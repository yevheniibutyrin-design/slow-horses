package store

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/yevheniibutyrin-design/slow-horses/internal/models"
)

// VotesCollectionName is the collection the vote queries run against.
const VotesCollectionName = "votes"

// ErrAlreadyVoted means this voter already has a vote in this market. It is
// returned WITH the vote they already cast, because the two cases the caller
// must tell apart — a retried request for the same outcome and an attempt to
// change a pick — differ only in which outcome that vote is on.
var ErrAlreadyVoted = errors.New("this voter has already voted in this market")

// Votes is the data access layer for the votes collection.
type Votes struct {
	coll *mongo.Collection
}

// NewVotes wires a store to the "votes" collection of the given database.
func NewVotes(db *mongo.Database) *Votes {
	return &Votes{coll: db.Collection(VotesCollectionName)}
}

// EnsureIndexes creates the indexes the vote queries depend on. Called once at
// startup; CreateMany is idempotent for identical specifications.
func (v *Votes) EnsureIndexes(ctx context.Context) error {
	_, err := v.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// "One vote per market per identity", enforced by the database rather
			// than by a check-then-write that two concurrent requests would both
			// pass. The OUTCOME is deliberately NOT part of this key: include it
			// and one voter could vote for every outcome in the market.
			Keys:    bson.D{{Key: "marketKey", Value: 1}, {Key: "voterId", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uniq_market_voter"),
		},
		{
			// The counts aggregation: $match on marketKey, $group by outcomeKey.
			Keys: bson.D{{Key: "marketKey", Value: 1}, {Key: "outcomeKey", Value: 1}},
		},
		{
			// The "voted today" counter.
			Keys: bson.D{{Key: "createdAt", Value: -1}},
		},
		{
			// The anonymous per-address rate limit. Partial, because an
			// authenticated vote stores no address and would only bloat it.
			Keys: bson.D{{Key: "ipHash", Value: 1}, {Key: "createdAt", Value: -1}},
			Options: options.Index().
				SetName("anon_ip_rate").
				SetPartialFilterExpression(bson.D{{Key: "ipHash", Value: bson.D{{Key: "$exists", Value: true}}}}),
		},
	})
	if err != nil {
		return fmt.Errorf("create vote indexes: %w", err)
	}
	return nil
}

// Counts returns the vote count per outcome for each requested market, as
// map[marketKeyHash]map[outcomeKeyHash]int64.
//
// ONE aggregation for the whole batch, not one query per market: the batched
// read exists precisely so a widget with five cards makes one request, and
// fanning it back out into five round trips inside the handler would give that
// back.
//
// A market with no votes is simply absent from the result — it is not an error,
// and the handler renders the zeros.
func (v *Votes) Counts(ctx context.Context, marketKeyHashes []string) (map[string]map[string]int64, error) {
	out := make(map[string]map[string]int64, len(marketKeyHashes))
	if len(marketKeyHashes) == 0 {
		return out, nil
	}

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "marketKey", Value: bson.D{{Key: "$in", Value: marketKeyHashes}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "market", Value: "$marketKey"},
				{Key: "outcome", Value: "$outcomeKey"},
			}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}

	cur, err := v.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate vote counts: %w", err)
	}

	var rows []struct {
		ID struct {
			Market  string `bson:"market"`
			Outcome string `bson:"outcome"`
		} `bson:"_id"`
		Count int64 `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode vote counts: %w", err)
	}

	for _, row := range rows {
		bucket, ok := out[row.ID.Market]
		if !ok {
			bucket = make(map[string]int64)
			out[row.ID.Market] = bucket
		}
		bucket[row.ID.Outcome] = row.Count
	}
	return out, nil
}

// CountSince counts every vote cast at or after the given instant — the header's
// "N voted today", which is site-wide and anonymous.
func (v *Votes) CountSince(ctx context.Context, since int64) (int64, error) {
	n, err := v.coll.CountDocuments(ctx, bson.D{
		{Key: "createdAt", Value: bson.D{{Key: "$gte", Value: since}}},
	})
	if err != nil {
		return 0, fmt.Errorf("count votes since %d: %w", since, err)
	}
	return n, nil
}

// CountAnonymousFromIPSince counts anonymous votes from one address since an
// instant. This is the only real guard on anonymous voting: a device token is
// free to discard, so uniqueness per device bounds nothing on its own.
func (v *Votes) CountAnonymousFromIPSince(ctx context.Context, ipHash string, since int64) (int64, error) {
	n, err := v.coll.CountDocuments(ctx, bson.D{
		{Key: "ipHash", Value: ipHash},
		{Key: "createdAt", Value: bson.D{{Key: "$gte", Value: since}}},
	})
	if err != nil {
		return 0, fmt.Errorf("count anonymous votes for address: %w", err)
	}
	return n, nil
}

// Cast records a vote, or reports the one this voter already cast in this
// market.
//
// The refusal comes from the unique index rather than from a preceding read:
// two simultaneous first votes from one voter would both pass a check-then-write
// and one of them has to lose, which is a property of the database and not of
// this function.
//
// On ErrAlreadyVoted the returned vote is the EXISTING one, so the handler can
// tell a retry of the same pick (idempotent success) from an attempt to change
// it (a conflict the client must be able to see).
func (v *Votes) Cast(ctx context.Context, vote models.Vote) (models.Vote, error) {
	_, err := v.coll.InsertOne(ctx, vote)
	if err == nil {
		return vote, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return models.Vote{}, fmt.Errorf("insert vote: %w", err)
	}

	existing, findErr := v.findByVoter(ctx, vote.MarketKeyHash, vote.VoterID)
	if findErr != nil {
		// The duplicate is the fact; failing to read back the loser's vote does
		// not turn it into a success.
		return models.Vote{}, fmt.Errorf("read back existing vote: %w", findErr)
	}
	return existing, ErrAlreadyVoted
}

// findByVoter reads this voter's vote in a market, if any.
func (v *Votes) findByVoter(ctx context.Context, marketKeyHash, voterID string) (models.Vote, error) {
	var vote models.Vote
	err := v.coll.FindOne(ctx, bson.D{
		{Key: "marketKey", Value: marketKeyHash},
		{Key: "voterId", Value: voterID},
	}).Decode(&vote)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Vote{}, ErrNotFound
	}
	if err != nil {
		return models.Vote{}, fmt.Errorf("find vote: %w", err)
	}
	return vote, nil
}
