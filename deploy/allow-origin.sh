#!/usr/bin/env bash
# Allow an extra origin to log in.
#
# Keycloak rejects any redirect_uri that is not on the client's allowlist, and
# the realm ships allowing only localhost. So the moment the app is reached at
# any other address — a phone on the LAN, a tunnel, a real domain — login fails
# with "Invalid parameter: redirect_uri" and nothing else explains why.
#
# The realm-export.json is only read on FIRST start (--import-realm); after
# that the realm lives in Postgres, so editing that file changes nothing. This
# patches the running client instead, which is what actually takes effect.
#
#   ./deploy/allow-origin.sh https://something.trycloudflare.com
#
# Idempotent: re-running with an origin that is already allowed is a no-op.
set -euo pipefail

ORIGIN="${1:-}"
if [ -z "$ORIGIN" ]; then
  echo "usage: $0 <origin>    e.g. $0 https://foo.trycloudflare.com" >&2
  exit 2
fi
ORIGIN="${ORIGIN%/}" # a trailing slash produces a redirect URI that never matches

KC_URL="${KC_URL:-http://localhost:8081}"
KC_ADMIN="${KC_ADMIN:-admin}"
KC_ADMIN_PASSWORD="${KC_ADMIN_PASSWORD:-admin}"
REALM="${REALM:-qomranote}"
CLIENT_ID="${CLIENT_ID:-qomranote-web}"

say() { printf '  %s\n' "$*"; }

say "authenticating against $KC_URL"
TOKEN="$(curl -fsS -X POST \
  "$KC_URL/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli" \
  -d "username=$KC_ADMIN" \
  -d "password=$KC_ADMIN_PASSWORD" \
  -d "grant_type=password" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"

if [ -z "$TOKEN" ]; then
  echo "could not get an admin token — is the stack up, and are the admin credentials right?" >&2
  exit 1
fi

UUID="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$KC_URL/admin/realms/$REALM/clients?clientId=$CLIENT_ID" \
  | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1)"

if [ -z "$UUID" ]; then
  echo "client $CLIENT_ID not found in realm $REALM" >&2
  exit 1
fi

CURRENT="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$KC_URL/admin/realms/$REALM/clients/$UUID")"

# Merge rather than replace: clobbering the list would break every origin that
# already worked, including localhost.
PATCHED="$(printf '%s' "$CURRENT" | python -c '
import json, sys
origin = sys.argv[1]
c = json.load(sys.stdin)
redirect = set(c.get("redirectUris") or [])
origins = set(c.get("webOrigins") or [])
redirect.add(origin + "/*")
origins.add(origin)
c["redirectUris"] = sorted(redirect)
c["webOrigins"] = sorted(origins)
json.dump(c, sys.stdout)
' "$ORIGIN")"

curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary "$PATCHED" \
  "$KC_URL/admin/realms/$REALM/clients/$UUID"

say "allowed $ORIGIN for $CLIENT_ID"
say "now start the stack with PUBLIC_ORIGIN=$ORIGIN so the token issuer matches"
