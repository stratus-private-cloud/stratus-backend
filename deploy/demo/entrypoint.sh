#!/bin/bash
#
# Runs the demo and resets it, both from inside the machine: every
# DEMO_RESET_EVERY seconds the server is stopped, its data directory emptied
# and a fresh one started and seeded. The data is in memory (see Dockerfile),
# so a machine that restarts for any other reason comes back the same way.
#
# A seed that fails leaves the server running rather than taking it down: a
# demo without its media is still a demo, and a machine that exited would be
# restarted by Fly into the same failure. scripts/demo-reset.sh, which every
# deploy runs, seeds it again from outside and fails loudly.

set -u

every="${DEMO_RESET_EVERY:-3600}"
port="${STRATUS_ADDR##*:}"
server=""
timer=""
stop=""

# Fly stops a machine with SIGINT by default and SIGTERM where told to; either
# ends the loop and goes to the server, which has something to shut down.
trap 'stop=1; kill -TERM $server $timer 2>/dev/null' TERM INT

status=0
while [ -z "$stop" ]; do
	rm -rf "${STRATUS_DATA_DIR:?}"
	mkdir -p "$STRATUS_DATA_DIR"

	/usr/local/bin/stratus "$@" &
	server=$!

	for _ in $(seq 60); do
		curl -fs -o /dev/null "http://localhost:$port/healthz" && break
		kill -0 "$server" 2>/dev/null || break
		sleep 1
	done

	if [ -z "$stop" ] && kill -0 "$server" 2>/dev/null; then
		SEED_BUNDLE=/demo/demo-media-v2.tar.gz /demo/seed-demo.sh "http://localhost:$port" ||
			echo "seeding the demo failed; it is running empty" >&2
	fi

	# In the background so the trap can interrupt it, and so a server that
	# dies early ends the wait instead of an hour of nothing answering.
	[ -z "$stop" ] && { sleep "$every" & timer=$!; }
	while [ -z "$stop" ] && kill -0 "$server" 2>/dev/null && kill -0 "$timer" 2>/dev/null; do
		wait -n "$server" "$timer"
	done
	kill "$timer" 2>/dev/null

	if ! kill -0 "$server" 2>/dev/null && [ -z "$stop" ]; then
		wait "$server"
		status=$?
		echo "the server exited with $status; the machine goes with it" >&2
		exit "$status"
	fi

	kill -TERM "$server" 2>/dev/null
	wait "$server"
	status=$?
	[ -z "$stop" ] && echo "resetting the demo" >&2
done
exit "$status"
