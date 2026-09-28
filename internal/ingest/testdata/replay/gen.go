// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// gen writes basic.jsonl, the synthetic journald capture replay's tests run on:
//
//	go run gen.go > basic.jsonl
//
// It is SYNTHETIC — ten virtual minutes shaped to contain four faults and a steady
// background, not a recording of anything. Every address is from the documentation
// ranges (RFC 5737 / RFC 3849) and every entry carries only the four fields replay reads,
// which TestReplayFixturesArePublishable enforces on everything in this directory.
//
// The faults, and what each is for:
//
//	t=200.2s  app  PRIORITY 3 "disk quota exceeded"   novel + ERROR + stderr
//	t=250.3s  app  PRIORITY 3 "INFO: shutting down"   source level beats the text (#24)
//	t=400s    db   40 retries in 4s over a 0.1/s base  an INFO-level burst
//	t=500.4s  kernel PRIORITY 1 "out of memory"       novel + FATAL
//
// and the background is one app request a second plus one db retry every ten seconds.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type entry struct {
	at   float64 // seconds from the start of the capture
	unit string
	pri  int
	msg  string
}

func main() {
	const start = int64(1_788_000_000_000_000) // 2026-08-29T10:40:00Z, in microseconds

	var es []entry
	for i := range 600 {
		es = append(es, entry{float64(i), "app.service", 6,
			fmt.Sprintf("request from 198.51.100.7 to api.example.com served id=%d in 12ms", i)})
	}
	for i := range 60 {
		es = append(es, entry{float64(i*10) + 0.5, "db.service", 6,
			fmt.Sprintf("db retry attempt %d to [2001:db8::10]:5432", i)})
	}
	for i := range 40 {
		es = append(es, entry{400 + float64(i)*0.1 + 0.05, "db.service", 6,
			fmt.Sprintf("db retry attempt %d to [2001:db8::10]:5432", 100+i)})
	}
	es = append(es,
		entry{200.2, "app.service", 3, "disk quota exceeded on /var/lib/app"},
		entry{250.3, "app.service", 3, "INFO: shutting down worker pool"},
		entry{500.4, "kernel", 1, "out of memory: killed process 4242"},
	)
	sort.SliceStable(es, func(i, j int) bool { return es[i].at < es[j].at })

	for _, e := range es {
		m := map[string]string{
			"__REALTIME_TIMESTAMP": fmt.Sprint(start + int64(e.at*1e6)),
			"PRIORITY":             fmt.Sprint(e.pri),
			"MESSAGE":              e.msg,
		}
		if e.unit == "kernel" {
			m["SYSLOG_IDENTIFIER"] = "kernel"
		} else {
			m["_SYSTEMD_UNIT"] = e.unit
		}
		b, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		os.Stdout.Write(append(b, '\n'))
	}
}
