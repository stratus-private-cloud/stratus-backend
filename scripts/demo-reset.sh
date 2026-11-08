#!/usr/bin/env bash
#
# Empties the demo instance and puts the demo media back: what every deploy
# does, and what somebody runs by hand to reset it now rather than on the hour.
# The hourly reset is the machine's own (deploy/demo/entrypoint.sh).
#
#   FLY_API_TOKEN=... [DEMO_BASE=https://...] scripts/demo-reset.sh <app> [image]
#
# **The wipe is a restart.** The data lives in the machine's memory, on
# /dev/shm (see deploy/demo/fly.toml), and the image seeds itself every time it
# starts (deploy/demo/Dockerfile), so with no image this restarts the machine
# and nothing else. With an image -- a deploy -- the machine is destroyed and a new
# one built on that digest instead; so is it when there is no machine to
# restart, which is what a reset that failed halfway leaves behind, and then it
# builds on what CI last published.
#
# Then it checks the wipe really happened: a file written before is gone
# after. A reset that kept anything would seed on top of it and look fine,
# which is how a schema change once landed on a database written by the build
# before it.
#
# And it seeds again from outside. The instance has already seeded itself, so
# this writes the same bytes over them; what it is for is the checks at the end
# of seed-demo.sh, which fail this job where somebody sees it rather than in a
# machine's log.

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

toml="$HERE/../deploy/demo/fly.toml"
setting() { sed -n "s/^ *$1 *= *\"\(.*\)\"/\1/p" "$toml" | head -1; }
user="$(setting STRATUS_USERNAME)"
pass="$(setting STRATUS_PASSWORD)"

# Every OpenSubsonic answer carries the version, including the refusal for
# asking with no credentials, so it says the server is up before signing in.
version() { curl -fsS "$BASE/rest/ping.view" 2>/dev/null | sed -n 's/.*serverVersion="\([^"]*\)".*/\1/p'; }

# Planted before the wipe, so that finding it afterwards means there was no
# wipe. Skipped when there is nothing to write it to, or no room: a full demo
# is one of the things a reset is for.
canary="reset-canary-$(date -u +%Y%m%d%H%M%S).txt"
old="$(version || true)"
if [ -z "$old" ] || ! printf 'planted by demo-reset.sh\n' |
	curl -fsS -o /dev/null -u "$user:$pass" -T - "$BASE/files/$canary"; then
	canary=""
fi

machines="$(flyctl machine list --app "$APP" --json | jq -r '.[].id')"
if [ -z "$IMAGE" ] && [ -n "$machines" ]; then
	for id in $machines; do
		flyctl machine restart "$id" --app "$APP"
	done
else
	for id in $machines; do
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

	flyctl deploy "$HERE/.." --config "$toml" --dockerfile "$HERE/../deploy/demo/Dockerfile" \
		${IMAGE:+--build-arg "IMAGE=$IMAGE"} --app "$APP" --ha=false --yes --wait-timeout 5m
fi

version=""
for _ in $(seq 60); do
	version="$(version || true)"
	[ -n "$version" ] && break
	sleep 2
done
[ -n "$version" ] || { echo "the instance did not say what it is running" >&2; exit 1; }

# The image seeds itself as it starts, and seeding from outside at the same
# time would race it to the playlist and make two. The last thing it writes is
# this event, so its being there means the inside is done writing.
for _ in $(seq 150); do
	curl -fsS -o /dev/null -u "$user:$pass" "$BASE/calendars/$user/default/gnome.ics" 2>/dev/null && break
	sleep 2
done

if [ -n "$canary" ]; then
	code="$(curl -s -o /dev/null -w '%{http_code}' -u "$user:$pass" "$BASE/files/$canary")"
	if [ "$code" != 404 ]; then
		echo "the reset kept what was there: /files/$canary answered $code" >&2
		exit 1
	fi
fi

echo "reseeding $APP: $version"
STRATUS_USERNAME="$user" STRATUS_PASSWORD="$pass" "$HERE/seed-demo.sh" "$BASE"
