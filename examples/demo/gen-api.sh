#!/bin/sh
# gen-api.sh — steady, healthy API traffic for the logscry demo.
#
# Everything here is ROUTINE and must stay below logscry's escalation threshold:
#   - INFO request lines on stdout carry no severity weight.
#   - the occasional ERROR on stderr scores stderr(0.3)+error(0.6)=0.9 < 1.0.
# So the tool stays silent on this service — that is the "silent on noise" half of
# the demo. The level prefix ("INFO:" / "ERROR:") is what logscry detects.
set -u
: "${INTERVAL:=1}"

i=0
while true; do
	i=$((i + 1))

	# A handful of paths so a few templates form and collapse (not one, not many).
	case $((i % 5)) in
	0) path=/api/users ;;
	1) path=/api/orders ;;
	2) path=/api/cart ;;
	3) path=/healthz ;;
	*) path=/api/search ;;
	esac

	# Mostly 200s, an occasional routine 404 — still nothing to escalate.
	status=200
	if [ $((i % 9)) -eq 0 ]; then status=404; fi
	dur=$(((i * 7) % 90 + 4))

	echo "INFO: request method=GET path=$path status=$status dur=${dur}ms"

	# Routine error chatter on stderr — the noise logscry must NOT cry wolf over.
	if [ $((i % 6)) -eq 0 ]; then
		echo "ERROR: cache miss key=session:$((i * 13)), falling back to db" 1>&2
	fi

	sleep "$INTERVAL"
done
