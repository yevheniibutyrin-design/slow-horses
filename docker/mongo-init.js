// Seeds the "rooms" collection with dummy bet rooms.
//
// Runs automatically on FIRST boot only: mongo's docker entrypoint executes
// files in /docker-entrypoint-initdb.d when the data directory is empty. A
// later `docker compose up` reuses the volume and skips this file — use
// `docker compose down -v` (or `make reseed`) to start clean.
//
// getSiblingDB is explicit on purpose: without it the script would target
// MONGO_INITDB_DATABASE (defaulting to "test") and the API would read an empty
// collection with no error to explain why.
const dbName = (typeof process !== 'undefined' && process.env && process.env.MONGO_INITDB_DATABASE) || 'slowhorses';
db = db.getSiblingDB(dbName);

if (!db.getCollectionNames().includes('rooms')) {
  db.createCollection('rooms');
}

// Every instant in this contract is epoch MILLISECONDS, never a Date or an
// RFC3339 string — the widget parses these fields as numbers.
const now = Date.now();
const inviteWindow = 30 * 60 * 1000;

// `subject` is the caller identity the API resolves from a bearer token. With
// AUTH_JWT_SECRET unset the token IS the subject, so `Authorization: Bearer
// seed-alice` acts as the participant below. It is stored and never serialised.
//
// Money is stored in MINOR UNITS: 1000 is 10.00. Odds are the feed's raw
// integers: 182 is 1.82.
const rooms = [
  {
    // An open duel waiting for someone to take the other side.
    _id: '11111111-1111-4111-8111-111111111111',
    eventId: 'evt-ucl-2026-09-15',
    strategy: 'duel',
    status: 'open',
    capacity: 2,
    participants: [
      {
        id: 'p-seed-alice',
        label: 'Maksym K.',
        initials: 'MK',
        // balances is Object<any>: shapes deliberately differ between players.
        balances: { eur: 75.5, freeBets: { count: 2, currency: 'EUR' } },
        betRef: { id: 'bet-seed-0001', number: 90001 },
        subject: 'seed-alice',
      },
    ],
    inviteCode: 'SEEDOPEN',
    inviteUrl: 'http://localhost:3000/event/evt-ucl-2026-09-15?betRoomId=11111111-1111-4111-8111-111111111111&betRoomInvite=SEEDOPEN',
    createdAt: now,
    expiresAt: now + inviteWindow,
    payload: {
      // The three selection keys are STRUCTURED objects, never opaque strings:
      // marketId is the widget's MarketModelType, marketItemId and each
      // outcomeId its key types. The lists inside them are [] and never null —
      // the widget matches them by JSON.stringify, where null equals nothing.
      marketId: { eventId: 'evt-ucl-2026-09-15', marketType: 5, period: 0, resultKind: 1 },
      marketItemId: { marketParameters: ['2.5'] },
      creatorSide: {
        outcomeId: { type: 3, values: [] },
        odd: 182,
        placement: { participantId: 'p-seed-alice', stake: 1000, lineItemId: 'li-seed-1', dataVersion: 7 },
      },
      opponentSide: { outcomeId: { type: 4, values: [] }, odd: 205 },
      // payout = round(10.00 x 1.82) = 18.20; entry = floor(18.20 / 2.05) = 8.87.
      figures: { payout: 1820, entry: 887, pot: 1887 },
    },
  },
  {
    // A filled duel: both bets placed, running to settlement. It stays filled
    // forever until a settlement worker exists — see docs/bet-room-api.md §5.6.
    _id: '22222222-2222-4222-8222-222222222222',
    eventId: 'evt-derby-2026-10-01',
    strategy: 'duel',
    status: 'filled',
    capacity: 2,
    participants: [
      {
        id: 'p-seed-carol',
        label: 'Carol D.',
        initials: 'CD',
        balances: { gbp: 20 },
        betRef: { id: 'bet-seed-0002', number: 90002 },
        subject: 'seed-carol',
      },
      {
        id: 'p-seed-dave',
        label: 'Dave E.',
        initials: 'DE',
        balances: {},
        betRef: { id: 'bet-seed-0003', number: 90003 },
        subject: 'seed-dave',
      },
    ],
    inviteCode: 'SEEDFULL',
    inviteUrl: 'http://localhost:3000/event/evt-derby-2026-10-01?betRoomId=22222222-2222-4222-8222-222222222222&betRoomInvite=SEEDFULL',
    createdAt: now - 10 * 60 * 1000,
    expiresAt: now + inviteWindow,
    payload: {
      marketId: { eventId: 'evt-derby-2026-10-01', marketType: 8, period: 0, resultKind: 1 },
      // Parameterless market: an EMPTY marketParameters is valid data, not a
      // missing field.
      marketItemId: { marketParameters: [] },
      creatorSide: {
        outcomeId: { type: 1, values: [] },
        odd: 190,
        placement: { participantId: 'p-seed-carol', stake: 2000, lineItemId: 'li-seed-2', dataVersion: 4 },
      },
      opponentSide: {
        outcomeId: { type: 2, values: [] },
        odd: 195,
        placement: { participantId: 'p-seed-dave', stake: 1948, lineItemId: 'li-seed-3', dataVersion: 5 },
      },
      // payout = round(20.00 x 1.90) = 38.00; entry = floor(38.00 / 1.95) = 19.48.
      figures: { payout: 3800, entry: 1948, pot: 3948 },
    },
  },
  {
    // Nobody took this one. The creator's bet stands as an ordinary single.
    _id: '33333333-3333-4333-8333-333333333333',
    eventId: 'evt-2026-09-04',
    strategy: 'duel',
    status: 'expired',
    capacity: 2,
    participants: [
      {
        id: 'p-seed-erin',
        label: 'Erin F.',
        initials: 'EF',
        balances: { usd: 250 },
        betRef: { id: 'bet-seed-0004', number: 90004 },
        subject: 'seed-erin',
      },
    ],
    inviteCode: 'SEEDGONE',
    inviteUrl: 'http://localhost:3000/event/evt-2026-09-04?betRoomId=33333333-3333-4333-8333-333333333333&betRoomInvite=SEEDGONE',
    createdAt: now - 2 * inviteWindow,
    expiresAt: now - inviteWindow,
    payload: {
      marketId: { eventId: 'evt-2026-09-04', marketType: 5, period: 0, resultKind: 1 },
      marketItemId: { marketParameters: ['3.5'] },
      creatorSide: {
        outcomeId: { type: 3, values: [] },
        odd: 260,
        placement: { participantId: 'p-seed-erin', stake: 500, lineItemId: 'li-seed-4', dataVersion: 2 },
      },
      opponentSide: { outcomeId: { type: 4, values: [] }, odd: 148 },
      // payout = round(5.00 x 2.60) = 13.00; entry = floor(13.00 / 1.48) = 8.78.
      figures: { payout: 1300, entry: 878, pot: 1378 },
    },
  },
];

// Guarded so re-running the script by hand through mongosh is harmless.
if (db.rooms.countDocuments() === 0) {
  db.rooms.insertMany(rooms);
  print('seeded ' + rooms.length + ' bet rooms into ' + dbName + '.rooms');
} else {
  print('rooms already present (' + db.rooms.countDocuments() + ' docs), skipping seed');
}
