#!/bin/sh
# gen-worker.sh — routine job traffic, then ONE genuine fault, for the logscry demo.
#
# For ~FAULT_AFTER seconds this is just healthy INFO traffic. Then it emits a single
# FATAL line — Postgres is unreachable — and keeps running. That FATAL scores on
# severity alone (fatal weight 1.0 >= threshold 1.0), so it is the one event logscry
# escalates to the LLM: the "speaks on signal" half of the demo.
#
# The fault is deliberately ONE line. logscry v1 templates one line at a time, so a
# multi-line stack trace would fragment into many templates (a known v1 limitation).
set -u
: "${INTERVAL:=1}"
: "${FAULT_AFTER:=18}"

i=0
while true; do
	i=$((i + 1))
	dur=$(((i * 11) % 40 + 3))

	echo "INFO: job=$i status=done queue=checkout dur=${dur}ms"

	# The one real fault: fires once, then routine traffic resumes so the stream
	# stays alive while logscry's explained card appears on the right.
	if [ "$i" -eq "$FAULT_AFTER" ]; then
		echo "FATAL: checkout worker: connect to postgres:5432 failed: dial tcp 172.20.0.9:5432: connect: connection refused" 1>&2
	fi

	sleep "$INTERVAL"
done
