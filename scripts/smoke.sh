#!/usr/bin/env sh
# Exercises every endpoint against a running stack. Usage: ./scripts/smoke.sh [baseUrl]
set -eu

BASE="${1:-http://localhost:5000}"

say() { printf '\n== %s\n' "$1"; }
# Extract a top-level string field from a JSON object without needing jq.
field() { sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" <<EOT
$1
EOT
}
status() { printf '   -> HTTP %s\n' "$1"; }

say "GET /ping"
PING="$(curl -sS -o /tmp/sh-ping -w '%{http_code}' "$BASE/ping")"
status "$PING"; cat /tmp/sh-ping; echo
[ "$PING" = "200" ] || { echo "FAIL: expected 200"; exit 1; }

say "GET /rooms"
ROOMS="$(curl -sS "$BASE/rooms")"
echo "$ROOMS" | head -c 400; echo
COUNT="$(printf '%s' "$ROOMS" | tr ',' '\n' | grep -c '"roomUrl"' || true)"
echo "   -> rooms: $COUNT"

say "PUT /room"
CREATED="$(curl -sS -X PUT "$BASE/room" -H 'content-type: application/json' \
  -d '{"name":"Smoke Test Room","eventId":"evt-smoke"}')"
echo "$CREATED"
ID="$(field "$CREATED" id)"
URL="$(field "$CREATED" roomUrl)"
[ -n "$ID" ] || { echo "FAIL: no id in create response"; exit 1; }
echo "   -> id: $ID"

say "POST /join (correct roomUrl)"
JOINED="$(curl -sS -o /tmp/sh-join -w '%{http_code}' -X POST "$BASE/join" -H 'content-type: application/json' \
  -d "{\"id\":\"$ID\",\"roomUrl\":\"$URL\",\"name\":\"Alice\",\"balance\":{\"usd\":100}}")"
status "$JOINED"; cat /tmp/sh-join; echo
[ "$JOINED" = "200" ] || { echo "FAIL: expected 200"; exit 1; }

say "POST /join (wrong roomUrl -> 403)"
DENIED="$(curl -sS -o /tmp/sh-deny -w '%{http_code}' -X POST "$BASE/join" -H 'content-type: application/json' \
  -d "{\"id\":\"$ID\",\"roomUrl\":\"$BASE/join/not-the-invite\",\"name\":\"Mallory\"}")"
status "$DENIED"; cat /tmp/sh-deny; echo
[ "$DENIED" = "403" ] || { echo "FAIL: expected 403"; exit 1; }

say "DELETE /room/$ID"
DEL="$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "$BASE/room/$ID")"
status "$DEL"
[ "$DEL" = "204" ] || { echo "FAIL: expected 204"; exit 1; }

say "DELETE /room/$ID again (-> 404)"
DEL2="$(curl -sS -o /tmp/sh-del -w '%{http_code}' -X DELETE "$BASE/room/$ID")"
status "$DEL2"; cat /tmp/sh-del; echo
[ "$DEL2" = "404" ] || { echo "FAIL: expected 404"; exit 1; }

say "GET /openapi.yaml"
SPEC="$(curl -sS -o /tmp/sh-spec -w '%{http_code}' "$BASE/openapi.yaml")"
status "$SPEC"; head -3 /tmp/sh-spec
[ "$SPEC" = "200" ] || { echo "FAIL: expected 200"; exit 1; }

printf '\nAll checks passed.\n'
