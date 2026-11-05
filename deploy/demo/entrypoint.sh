#!/bin/bash
#
# Starts the server and seeds it, every time the machine starts: the data is in
# memory, so every start is an empty server (see Dockerfile).
#
# A seed that fails leaves the server running rather than taking it down: a
# demo without its media is still a demo, and a machine that exited would be
# restarted by Fly into the same failure. scripts/demo-reset.sh seeds it again
# from outside and fails loudly, which is where somebody finds out.

set -u

/usr/local/bin/stratus "$@" &
server=$!

# Fly stops a machine with SIGINT by default and SIGTERM where told to; either
# goes to the server, which is the process that has something to shut down.
trap 'kill -TERM "$server" 2>/dev/null' TERM INT

port="${STRATUS_ADDR##*:}"
for _ in $(seq 60); do
	curl -fs -o /dev/null "http://localhost:$port/healthz" && break
	kill -0 "$server" 2>/dev/null || break
	sleep 1
done

if kill -0 "$server" 2>/dev/null; then
	SEED_BUNDLE=/demo/demo-media-v2.tar.gz /demo/seed-demo.sh "http://localhost:$port" ||
		echo "seeding the demo failed; it is running empty" >&2
fi

# The first wait returns when the trap fires; the second is the server exiting.
wait "$server"
status=$?
kill -0 "$server" 2>/dev/null && { wait "$server"; status=$?; }
exit "$status"
