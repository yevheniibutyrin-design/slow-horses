#!/usr/bin/env sh
# Fills the rooms collection with ten open duels, through the real API.
# Usage: ./scripts/seed.sh [baseUrl]
#
# Goes through POST /rooms rather than inserting documents, so every row is one
# the service would actually produce: figures derived by internal/duel, invite
# codes from crypto/rand, and the underround, per-duel-max and daily-limit
# guards all enforced. A hand-written document can drift from that; a seeded one
# cannot.
#
# ADDITIVE. Running it twice leaves twenty rooms. `make seed-fresh` empties the
# collection first.
#
# With AUTH_JWT_SECRET unset the bearer token IS the caller's identity, so the
# subjects below are ten rooms across five distinct people. That matters: a
# caller's own rooms are excluded from their own open-rooms list, so seeding
# everything as one person would make GET /rooms look empty to that person.
set -eu

BASE="${1:-http://localhost:5000}"
RUN="$(date +%s)"

# Inside the 30 minute invite window, so the server does not clamp it. Rooms
# seeded now therefore stop being open in 25 minutes -- re-run to refresh.
EXPIRES=$(( ($(date +%s) + 1500) * 1000 ))

say() { printf '\n== %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1"; exit 1; }

# Extract a string field without needing jq. Splits on commas first so each
# field lands on its own line: the response nests participants and bet
# references, and a greedy match would return the LAST "id" rather than the
# room's.
field() {
  printf '%s' "$1" | tr ',' '\n' \
    | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" \
    | head -1
}

printf 'Seeding %s\n' "$BASE"

# Sanity-check the target before posting ten rooms at a server that is not there.
PING="$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/ping" || true)"
[ "$PING" = "200" ] || fail "no API at $BASE (GET /ping returned '$PING') -- is the stack up?"

N=0
CREATED=0

# subject | event | market | item | outcomeA | oddA | outcomeB | oddB | stake | label | initials | balances
#
# Odds are the feed's raw integers: 182 is 1.82. Every pair satisfies the
# underround guard 100*(a+b) >= a*b, three of them at exact equality (200/200,
# 350/140, 125/500), which is permitted and worth having in test data.
#
# Balances are Object<any> on purpose: the shapes differ between players.
while IFS='|' read -r SUBJ EVENT MARKET ITEM OUTA ODDA OUTB ODDB STAKE LABEL INITIALS BALANCES; do
  case "$SUBJ" in ''|\#*) continue ;; esac
  N=$(( N + 1 ))

  BODY="$(cat <<JSON
{"eventId":"$EVENT","strategy":"duel",
 "participant":{"label":"$LABEL","initials":"$INITIALS","balances":$BALANCES},
 "betRef":{"id":"bet-seed-$RUN-$N","number":$(( 20000 + N ))},
 "expiresAt":$EXPIRES,
 "payload":{"marketId":"$MARKET","marketItemId":"$ITEM",
   "creatorSide":{"outcomeId":"$OUTA","odd":$ODDA,
     "placement":{"stake":$STAKE,"lineItemId":"li-seed-$RUN-$N","dataVersion":1}},
   "opponentSide":{"outcomeId":"$OUTB","odd":$ODDB}}}
JSON
)"

  STATUS="$(curl -sS -o /tmp/seed-body -w '%{http_code}' -X POST "$BASE/rooms" \
    -H 'content-type: application/json' -H "authorization: Bearer $SUBJ" -d "$BODY")"
  RESP="$(cat /tmp/seed-body)"

  if [ "$STATUS" != "201" ]; then
    printf '  %2d. FAILED (HTTP %s) %s\n      %s\n' "$N" "$STATUS" "$EVENT" "$RESP"
    fail "room $N was refused -- nothing further was seeded"
  fi

  ROOM="$(field "$RESP" id)"
  CODE="$(field "$RESP" inviteCode)"
  CREATED=$(( CREATED + 1 ))
  printf '  %2d. %-16s %-20s %s @ %s vs %s  stake %-6s code %s\n' \
    "$N" "$SUBJ" "$EVENT" "$OUTA" "$ODDA" "$ODDB" "$STAKE" "$CODE"
  printf '      %s\n' "$ROOM"
done <<'TABLE'
seed-alice  | evt-ucl-final     | total_goals      | total_goals_2.5   | over  | 182 | under | 205 | 10.00  | Maksym K.  | MK | {"eur":75.5}
seed-bohdan | evt-ucl-final     | match_result     | match_result_1x2  | home  | 210 | away  | 190 | 25.00  | Ivan P.    | IP | {"eur":240.00}
seed-olena  | evt-ucl-final     | both_teams_score | btts_main         | yes   | 200 | no    | 200 | 5.00   | Olena H.   | OH | {"eur":18.2,"bonus":5}
seed-taras  | evt-ucl-final     | total_corners    | total_corners_9.5 | over  | 250 | under | 166 | 50.00  | Taras B.   | TB | {"eur":610}
seed-iryna  | evt-derby-london  | match_result     | match_result_1x2  | home  | 190 | draw  | 195 | 15.00  | Iryna D.   | ID | {"eur":92.75}
seed-alice  | evt-derby-london  | total_goals      | total_goals_3.5   | over  | 320 | under | 145 | 12.50  | Maksym K.  | MK | {"eur":75.5}
seed-bohdan | evt-derby-london  | first_scorer     | first_scorer_any  | yes   | 350 | no    | 140 | 7.50   | Ivan P.    | IP | {"eur":240.00}
seed-olena  | evt-open-final    | set_winner       | set_winner_1      | p1    | 133 | p2    | 400 | 100.00 | Olena H.   | OH | {"eur":18.2,"bonus":5}
seed-taras  | evt-open-final    | total_games      | total_games_22.5  | over  | 290 | under | 152 | 20.00  | Taras B.   | TB | {"eur":610}
seed-iryna  | evt-open-final    | match_result     | match_result_1x2  | home  | 125 | away  | 500 | 500.00 | Iryna D.   | ID | {"eur":92.75}
TABLE

say "Seeded $CREATED rooms"

# Prove the rooms are actually listable, which is the thing seed data is for.
# A subject who created none of them sees all ten; that is also the check that
# the open-rooms route and the expiry clamp behaved.
TOTAL=0
for EVENT in evt-ucl-final evt-derby-london evt-open-final; do
  BODY="$(curl -sS -H 'authorization: Bearer seed-spectator' "$BASE/rooms?eventId=$EVENT&status=open")"
  COUNT="$(printf '%s' "$BODY" | tr ',' '\n' | grep -c '"strategy":"duel"' || true)"
  TOTAL=$(( TOTAL + COUNT ))
  printf '  %-18s %s open\n' "$EVENT" "$COUNT"
done
printf '  %-18s %s open\n' "TOTAL" "$TOTAL"
[ "$TOTAL" = "10" ] || fail "expected 10 listable open rooms, found $TOTAL"

say "Done"
printf 'Browse: curl -s -H "authorization: Bearer seed-spectator" "%s/rooms?eventId=evt-ucl-final&status=open"\n' "$BASE"
