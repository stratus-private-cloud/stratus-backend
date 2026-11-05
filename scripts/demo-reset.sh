#!/usr/bin/env bash
#
# Empties the demo instance and puts the demo media back: what every deploy and
# the hourly reset do, in one place so the two cannot drift.
#
#   FLY_API_TOKEN=... [DEMO_BASE=https://...] scripts/demo-reset.sh <app> [image]
#
# **The wipe is destroying the machine.** The data lives in its memory, on
# /dev/shm (see deploy/demo/fly.toml), so a new machine is a new, empty disk.
#
# Then it checks the tree really is empty before seeding. A reset that kept
# anything would seed on top of it and look fine, which is how a schema change
# once landed on a database written by the build before it.
#
# Without an image it deploys what deploy/demo/fly.toml names, which is what
# CI last published.

set -euo pipefail

APP="${1:?usage: demo-reset.sh <app> [image]}"
IMAGE="${2:-}"
# Where the demo is seeded and then checked from. The app's own Fly name unless
# DEMO_BASE says otherwise: an instance with a domain of its own is worth
# checking through the address people are given, so a certificate that expired
# or a DNS record somebody moved shows up on the next hourly reset rather than
# when somebody tries the link. Unset falls back, which is what a fork gets.
BASE="${DEMO_BASE:-https://$APP.fly.dev}"
BASE="${BASE%/}"
HERE="$(cd "$(dirname "$0")" && pwd)"

for id in $(flyctl machine list --app "$APP" --json | jq -r '.[].id'); do
	flyctl machine destroy "$id" --app "$APP" --force
done

# The demo had a volume until it moved to /dev/shm, and one left behind is
# still billed. A volume stays attached for a moment after its machine is
# destroyed, and refuses to go until it is not.
for id in $(flyctl volumes list --app "$APP" --json | jq -r '.[].id'); do
	for attempt in 1 2 3 4 5 6; do
		flyctl volumes destroy "$id" --app "$APP" --yes && break
		[ "$attempt" -lt 6 ] || exit 1
		sleep 5
	done
done

toml="$HERE/../deploy/demo/fly.toml"
setting() { sed -n "s/^ *$1 *= *\"\(.*\)\"/\1/p" "$toml" | head -1; }

flyctl deploy --config "$toml" --app "$APP" ${IMAGE:+--image "$IMAGE"} --ha=false --yes --wait-timeout 5m

# The version is the password, and every OpenSubsonic answer carries it,
# including the refusal for asking with no credentials.
version="$(curl -fsS "$BASE/rest/ping.view" | sed -n 's/.*serverVersion="\([^"]*\)".*/\1/p')"
[ -n "$version" ] || { echo "the instance did not say what it is running" >&2; exit 1; }
user="$(setting STRATUS_USERNAME)"

# The writable tree, which is what a reseed would be writing into -- not the
# origin, whose listing is the three collections and is never empty (#279).
# The one thing allowed in it is the backup taken at startup, which is a file
# in the tree since #436.
listing="$(curl -fsS -u "$user:$version" -X PROPFIND -H "Depth: 1" "$BASE/files/")"
hrefs="$(grep -o '<D:href>[^<]*' <<<"$listing" | grep -v "^<D:href>/files/$(setting STRATUS_BACKUP_DIR)/")"
if [ "$(wc -l <<<"$hrefs")" -ne 1 ]; then
	echo "the tree is not empty after the reset; not seeding on top of it:" >&2
	echo "$hrefs" >&2
	exit 1
fi

echo "reseeding $APP: $version"
STRATUS_USERNAME="$user" STRATUS_PASSWORD="$version" "$HERE/seed-demo.sh" "$BASE"
