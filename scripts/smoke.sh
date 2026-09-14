#!/usr/bin/env sh
# Drives a full duel against a running stack, in the order the widget does.
# Usage: ./scripts/smoke.sh [baseUrl]
#
# Two callers: ALICE creates a duel from a bet she has already placed, BOB takes
# the other side. With AUTH_JWT_SECRET unset the server treats the bearer token
# itself as the caller's identity, which is what makes "alice" and "bob" below
# two different people. Set the secret and this script needs real tokens.
set -eu

BASE="${1:-http://localhost:5000}"
ALICE="alice-smoke"
BOB="bob-smoke"
EVENT="evt-smoke-$(date +%s)"

say() { printf '\n== %s\n' "$1"; }
status() { printf '   -> HTTP %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1"; exit 1; }

# Extract a string field without needing jq.
#
# Splits on commas first so each field lands on its own line: the response now
# nests participants and bet references, and a greedy match over the whole body
# would return the LAST "id" it finds rather than the room's.
field() {
  printf '%s' "$1" | tr ',' '\n' \
    | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" \
    | head -1
}
# call METHOD PATH TOKEN [BODY] -> body in $BODY, status in $STATUS
call() {
  _method="$1"; _path="$2"; _token="$3"; _body="${4:-}"
  if [ -n "$_body" ]; then
    STATUS="$(curl -sS -o /tmp/sh-body -w '%{http_code}' -X "$_method" "$BASE$_path" \
      -H 'content-type: application/json' -H "authorization: Bearer $_token" -d "$_body")"
  else
    STATUS="$(curl -sS -o /tmp/sh-body -w '%{http_code}' -X "$_method" "$BASE$_path" \
      -H "authorization: Bearer $_token")"
  fi
  BODY="$(cat /tmp/sh-body)"
}

say "GET /ping (public)"
STATUS="$(curl -sS -o /tmp/sh-body -w '%{http_code}' "$BASE/ping")"
status "$STATUS"; cat /tmp/sh-body; echo
[ "$STATUS" = "200" ] || fail "expected 200"

say "GET /rooms/limits without a credential -> 401"
STATUS="$(curl -sS -o /tmp/sh-body -w '%{http_code}' "$BASE/rooms/limits")"
status "$STATUS"; cat /tmp/sh-body; echo
[ "$STATUS" = "401" ] || fail "expected 401 — every room route needs a bearer credential"
# The widget reads body.message; an "error" key would silently drop every reason.
grep -q '"message"' /tmp/sh-body || fail 'error body has no "message" key'
if grep -q '"error"' /tmp/sh-body; then fail 'error body still uses the old "error" key'; fi

say "GET /rooms/limits as alice"
call GET /rooms/limits "$ALICE"
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "200" ] || fail "expected 200"

say "POST /rooms — alice opens a duel: 10.00 on Over 2.5 @ 1.82, opposite 2.05"
call POST /rooms "$ALICE" "$(cat <<JSON
{"eventId":"$EVENT","strategy":"duel",
 "participant":{"label":"Maksym K.","initials":"MK","balances":{"eur":75.5}},
 "betRef":{"id":"bet-smoke-$(date +%s)-a","number":12345},
 "expiresAt":$(( ($(date +%s) + 1800) * 1000 )),
 "payload":{"marketId":"total_goals","marketItemId":"total_goals_2.5",
   "creatorSide":{"outcomeId":"over","odd":182,
     "placement":{"stake":10.00,"lineItemId":"li-12","dataVersion":7}},
   "opponentSide":{"outcomeId":"under","odd":205},
   "figures":{"payout":0,"entry":0,"pot":0}}}
JSON
)"
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "201" ] || fail "expected 201"

ROOM="$(field "$BODY" id)"
CODE="$(field "$BODY" inviteCode)"
INVITE="$(field "$BODY" inviteUrl)"
[ -n "$ROOM" ] || fail "no room id in the create response"
[ -n "$CODE" ] || fail "no invite code was issued"
echo "   -> room: $ROOM  code: $CODE"
echo "   -> invite: $INVITE"

# The figures are derived by the service, not taken from the request.
case "$BODY" in
  *'"payout":18.20'*) ;;
  *) fail "payout is not 18.20 — the duel arithmetic is wrong" ;;
esac
case "$BODY" in
  *'"entry":8.87'*) ;;
  *) fail "entry is not 8.87 — it must be floored from the rounded payout" ;;
esac
echo "   -> figures: payout 18.20, entry 8.87, pot 18.87"

say "POST /rooms/{id}/read — a stranger with no code watches, but gets no secret"
call POST "/rooms/$ROOM/read" carol-smoke '{}'
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "200" ] || fail "expected 200 — a room is observable without a credential"
case "$BODY" in
  *'"inviteCode"'*) fail "the stranger was handed the invite code" ;;
esac
case "$BODY" in
  *'"inviteUrl"'*) fail "the stranger was handed the invite url, which embeds the code" ;;
esac
echo "   -> state readable, inviteCode and inviteUrl both withheld"

say "POST /rooms/{id}/read — no Authorization header at all"
STATUS="$(curl -sS -o /tmp/sh-body -w '%{http_code}' -X POST "$BASE/rooms/$ROOM/read" \
  -H 'content-type: application/json' -d '{}')"
status "$STATUS"
[ "$STATUS" = "200" ] || fail "expected 200 — an anonymous caller may read a room"
echo "   -> anonymous read works"

say "POST /rooms/{id}/read — alice reads her own room"
call POST "/rooms/$ROOM/read" "$ALICE" '{}'
status "$STATUS"
[ "$STATUS" = "200" ] || fail "expected 200"
case "$BODY" in
  *'"viewerParticipantId"'*) echo "   -> viewerParticipantId is present" ;;
  *) fail "the reader is not told which participant they are" ;;
esac

say "GET /rooms?eventId=... — bob sees alice's open duel"
call GET "/rooms?eventId=$EVENT&status=open" "$BOB"
status "$STATUS"; echo "$BODY" | head -c 200; echo
[ "$STATUS" = "200" ] || fail "expected 200"
case "$BODY" in
  *"$ROOM"*) echo "   -> the duel is listed" ;;
  *) fail "the open duel was not listed for another player" ;;
esac

say "GET /rooms?eventId=... — alice does not see her own"
call GET "/rooms?eventId=$EVENT&status=open" "$ALICE"
[ "$BODY" = "[]" ] || fail "the caller's own rooms must be excluded (got: $BODY)"
echo "   -> []"

say "POST /rooms/{id}/seat — wrong invite code -> 403"
call POST "/rooms/$ROOM/seat" "$BOB" '{"inviteCode":"WRONGCOD"}'
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "403" ] || fail "expected 403"

say "POST /rooms/{id}/seat — alice cannot take a second seat -> 409"
call POST "/rooms/$ROOM/seat" "$ALICE" "{\"inviteCode\":\"$CODE\"}"
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "409" ] || fail "expected 409"

say "POST /rooms/{id}/seat — bob holds the seat"
call POST "/rooms/$ROOM/seat" "$BOB" "{\"inviteCode\":\"$CODE\"}"
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "201" ] || fail "expected 201"
HOLD="$(field "$BODY" id)"
[ -n "$HOLD" ] || fail "no hold id"
echo "   -> hold: $HOLD"

say "POST /rooms/{id}/seat — the held seat is not offered to carol -> 409"
call POST "/rooms/$ROOM/seat" carol-smoke "{\"inviteCode\":\"$CODE\"}"
status "$STATUS"
[ "$STATUS" = "409" ] || fail "expected 409"

say "DELETE /rooms/{id}/seat/{hold} — bob releases, then takes it again"
call DELETE "/rooms/$ROOM/seat/$HOLD" "$BOB"
status "$STATUS"
[ "$STATUS" = "204" ] || fail "expected 204"
call POST "/rooms/$ROOM/seat" "$BOB" "{\"inviteCode\":\"$CODE\"}"
[ "$STATUS" = "201" ] || fail "the seat did not reopen after an explicit release"
HOLD="$(field "$BODY" id)"
echo "   -> seat reopened immediately; new hold: $HOLD"

say "POST /rooms/{id}/seat/{hold}/confirm — bob's bet is placed, the room fills"
call POST "/rooms/$ROOM/seat/$HOLD/confirm" "$BOB" "$(cat <<JSON
{"participant":{"label":"Ivan P.","initials":"IP","balances":{"eur":40}},
 "betRef":{"id":"bet-smoke-$(date +%s)-b","number":12346},
 "odd":205,
 "placement":{"stake":8.87,"lineItemId":"li-99","dataVersion":9}}
JSON
)"
status "$STATUS"; echo "$BODY" | head -c 400; echo
[ "$STATUS" = "200" ] || fail "expected 200"
case "$BODY" in
  *'"status":"filled"'*) echo "   -> the duel is filled" ;;
  *) fail "the room did not report as filled" ;;
esac

say "POST /rooms/{id}/rematch — alice re-offers to bob"
call POST "/rooms/$ROOM/rematch" "$ALICE" "$(cat <<JSON
{"participant":{"label":"Maksym K.","initials":"MK","balances":{"eur":65}},
 "betRef":{"id":"bet-smoke-$(date +%s)-c","number":12400},
 "payload":{"marketId":"total_goals","marketItemId":"total_goals_2.5",
   "creatorSide":{"outcomeId":"over","odd":182,
     "placement":{"stake":10.00,"lineItemId":"li-77","dataVersion":3}},
   "opponentSide":{"outcomeId":"under","odd":205},
   "figures":{"payout":0,"entry":0,"pot":0}}}
JSON
)"
status "$STATUS"; echo "$BODY" | head -c 300; echo
[ "$STATUS" = "201" ] || fail "expected 201"
REMATCH="$(field "$BODY" id)"
REMATCH_CODE="$(field "$BODY" inviteCode)"

say "POST /rooms/{rematch}/seat — carol has the code and is still refused -> 403"
call POST "/rooms/$REMATCH/seat" carol-smoke "{\"inviteCode\":\"$REMATCH_CODE\"}"
status "$STATUS"; echo "$BODY"
[ "$STATUS" = "403" ] || fail "a rematch must be joinable only by the original opponent"

say "POST /rooms/{rematch}/seat — bob, the original opponent, can"
call POST "/rooms/$REMATCH/seat" "$BOB" "{\"inviteCode\":\"$REMATCH_CODE\"}"
status "$STATUS"
[ "$STATUS" = "201" ] || fail "expected 201"

say "GET /rooms?eventId=... — the rematch is never publicly listed"
call GET "/rooms?eventId=$EVENT&status=open" carol-smoke
[ "$BODY" = "[]" ] || fail "a rematch room was listed publicly (got: $BODY)"
echo "   -> []"

say "GET /openapi.yaml (public)"
STATUS="$(curl -sS -o /tmp/sh-body -w '%{http_code}' "$BASE/openapi.yaml")"
status "$STATUS"; head -3 /tmp/sh-body
[ "$STATUS" = "200" ] || fail "expected 200"

printf '\nAll checks passed.\n'
