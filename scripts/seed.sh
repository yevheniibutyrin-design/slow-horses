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

# subject | event | marketType | marketParameters | outcomeTypeA | oddA | outcomeTypeB | oddB | stake | label | initials | balances
#
# The three selection keys are STRUCTURED, not opaque strings: the market is a
# MarketModelType (its eventId is the room's own event), the item is a list of
# market parameters -- legitimately EMPTY for a parameterless market such as
# 1X2 -- and each outcome is a type plus its own values.
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
 "payload":{"marketId":{"eventId":"$EVENT","marketType":$MARKET,"period":0,"resultKind":1},
   "marketItemId":{"marketParameters":$ITEM},
   "creatorSide":{"outcomeId":{"type":$OUTA,"values":[]},"odd":$ODDA,
     "placement":{"stake":$STAKE,"lineItemId":"li-seed-$RUN-$N","dataVersion":1}},
   "opponentSide":{"outcomeId":{"type":$OUTB,"values":[]},"odd":$ODDB}}}
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
  printf '  %2d. %-16s %-20s type %s @ %s vs type %s @ %s  stake %-6s code %s\n' \
    "$N" "$SUBJ" "$EVENT" "$OUTA" "$ODDA" "$OUTB" "$ODDB" "$STAKE" "$CODE"
  printf '      %s\n' "$ROOM"
done <<'TABLE'
seed-alice  | evt-ucl-final     |  5 | ["2.5"]  | 3 | 182 | 4 | 205 | 10.00  | Maksym K.  | MK | {"eur":75.5}
seed-bohdan | evt-ucl-final     |  1 | []       | 1 | 210 | 3 | 190 | 25.00  | Ivan P.    | IP | {"eur":240.00}
seed-olena  | evt-ucl-final     |  8 | []       | 1 | 200 | 2 | 200 | 5.00   | Olena H.   | OH | {"eur":18.2,"bonus":5}
seed-taras  | evt-ucl-final     | 12 | ["9.5"]  | 3 | 250 | 4 | 166 | 50.00  | Taras B.   | TB | {"eur":610}
seed-iryna  | evt-derby-london  |  1 | []       | 1 | 190 | 2 | 195 | 15.00  | Iryna D.   | ID | {"eur":92.75}
seed-alice  | evt-derby-london  |  5 | ["3.5"]  | 3 | 320 | 4 | 145 | 12.50  | Maksym K.  | MK | {"eur":75.5}
seed-bohdan | evt-derby-london  | 21 | []       | 1 | 350 | 2 | 140 | 7.50   | Ivan P.    | IP | {"eur":240.00}
seed-olena  | evt-open-final    | 30 | ["1"]    | 1 | 133 | 3 | 400 | 100.00 | Olena H.   | OH | {"eur":18.2,"bonus":5}
seed-taras  | evt-open-final    | 15 | ["22.5"] | 3 | 290 | 4 | 152 | 20.00  | Taras B.   | TB | {"eur":610}
seed-iryna  | evt-open-final    |  1 | []       | 1 | 125 | 3 | 500 | 500.00 | Iryna D.   | ID | {"eur":92.75}
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
