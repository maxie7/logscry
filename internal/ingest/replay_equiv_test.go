// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
	"github.com/maxie7/logscry/internal/score"
)

// groupTimeout is the default multi-line idle flush, which both legs of every comparison
// here run with — the coalescer is part of the program being compared.
const groupTimeout = 200 * time.Millisecond

// flagged is what the no-drift comparison holds equal: which template escalated, in what
// order, for which KINDS of reason. The digits inside a reason ("burst: 8.3/s", "unseen for
// 2s") are measured by whichever clock ran, so they are expected to differ in the last
// place between wall and capture time, and are deliberately not compared.
type flagged struct {
	Pattern string
	Kinds   []string
}

func reasonKind(r string) string {
	switch {
	case strings.HasPrefix(r, "novel template (first seen)"):
		return "novel:first"
	case strings.HasPrefix(r, "novel template (unseen"):
		return "novel:cooloff"
	case strings.HasPrefix(r, "burst:"):
		return "burst"
	}
	return r // "stderr", "level ERROR": no measured digits in them
}

// runLeg drives source → coalescer → pipeline and returns the escalations and the gate
// counters. sourceTime selects replay's clock for both the coalescer and the pipeline;
// without it this is the live program, clocked by the wall.
func runLeg(t *testing.T, src ingest.Source, cfg score.Config, sourceTime bool) ([]flagged, score.Stats) {
	t.Helper()
	ctx := context.Background()
	lines := make(chan model.LogLine, 1024)
	go func() {
		defer close(lines)
		if err := ingest.Run(ctx, []ingest.Source{src}, lines); err != nil {
			t.Errorf("ingest: %v", err)
		}
	}()
	grouped := make(chan model.LogLine, 1024)
	if sourceTime {
		go pipeline.CoalesceSourceTime(ctx, lines, grouped, groupTimeout)
	} else {
		go pipeline.Coalesce(ctx, lines, grouped, groupTimeout)
	}
	events := make(chan pipeline.Event, 1024)
	sc := score.New(cfg, nil)
	go pipeline.Run(ctx, grouped, pipeline.Options{Events: events, Scorer: sc, SourceTime: sourceTime})

	var out []flagged
	for ev := range events {
		if !ev.Escalate {
			continue
		}
		f := flagged{Pattern: ev.Pattern}
		for _, r := range ev.Reasons {
			f.Kinds = append(f.Kinds, reasonKind(r))
		}
		out = append(out, f)
	}
	return out, sc.Stats()
}

// shortConfig shrinks every window so a real-time leg fits in six seconds. Validated, so a
// shrink that broke an invariant fails here rather than silently scoring nothing.
func shortConfig(t *testing.T) score.Config {
	t.Helper()
	cfg := score.Defaults()
	cfg.Warmup, cfg.WarmupLines = 500*time.Millisecond, 5
	cfg.Cooloff = 1500 * time.Millisecond
	cfg.BaselineMinAge, cfg.BaselineMinCount = time.Second, 5
	cfg.BurstWindow, cfg.BurstMultiplier, cfg.BurstMinCount = time.Second, 2, 8
	cfg.CacheTTL = time.Second
	cfg.RatePerMin = 7
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// shortCapture is six seconds shaped to hit every time-dependent path under shortConfig,
// each decision at least ~100ms from the edge of the window that decides it:
//
//	0.8s   ERROR on stderr, first seen            novel + severity
//	2.0s   a three-line traceback                  coalesced into one novel ERROR
//	3.0s   another novel ERROR
//	3.5s   the 0.8s ERROR again, 2.7s later        cooloff novelty, cache expired
//	4.0s   FATAL from the kernel                   severity alone
//	5.2s+  fifteen retries 30ms apart over a 1/s base  a burst
//	5.8s   FATAL, a new template                   the last token
//	5.9s   FATAL, another new template             suppressed: the bucket is empty
func shortCapture(t *testing.T) string {
	t.Helper()
	const t0 = int64(1_788_000_000_000_000)
	type e struct {
		at   float64
		unit string
		pri  int
		msg  string
	}
	var es []e
	for i := range 60 {
		es = append(es, e{float64(i) * 0.1, "app.service", 6, fmt.Sprintf("tick id=%d", i)})
	}
	for i := range 5 {
		es = append(es, e{0.5 + float64(i), "db.service", 6, fmt.Sprintf("retry attempt %d", i)})
	}
	for i := range 15 {
		es = append(es, e{5.2 + float64(i)*0.03, "db.service", 6, fmt.Sprintf("retry attempt %d", 100+i)})
	}
	es = append(es,
		e{0.8, "cache.service", 3, "cache miss storm on shard"},
		e{2.0, "worker.service", 3, "Traceback (most recent call last):"},
		e{2.01, "worker.service", 3, `  File "/srv/worker/x.py", line 3, in <module>`},
		e{2.02, "worker.service", 3, "ValueError: bad input"},
		e{3.0, "app.service", 3, "disk quota exceeded on /var/lib/app"},
		e{3.5, "cache.service", 3, "cache miss storm on shard"},
		e{4.0, "kernel", 0, "out of memory: killed process 4242"},
		e{5.8, "kernel", 0, "general protection fault in module alpha"},
		e{5.9, "kernel", 0, "watchdog: soft lockup on cpu"},
	)
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].at < es[j-1].at; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
	var b strings.Builder
	for _, x := range es {
		unitKey := "_SYSTEMD_UNIT"
		if x.unit == "kernel" {
			unitKey = "SYSLOG_IDENTIFIER"
		}
		fmt.Fprintf(&b, `{"MESSAGE":%q,"PRIORITY":"%d",%q:%q,"__REALTIME_TIMESTAMP":"%d"}`+"\n",
			x.msg, x.pri, unitKey, x.unit, t0+int64(x.at*1e6))
	}
	path := filepath.Join(t.TempDir(), "short.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReplayAtOneXMatchesWallClock is the no-drift property measured against the thing it
// claims to reproduce. The live leg is today's program — coalescer and pipeline on the wall
// clock — fed the capture in real time. The replay leg is the same capture at max speed on
// the capture's clock. They must flag the same templates, in the same order, for the same
// kinds of reason, with the same gate counters.
//
// What is NOT compared, because it is expected to differ: the digits inside a reason, and
// every absolute timestamp (wall against capture). Scheduling lag in the live leg shifts all
// of its timestamps by a common delay; the capture keeps every decision away from a window
// edge by ~100ms or more so that jitter cannot flip one.
func TestReplayAtOneXMatchesWallClock(t *testing.T) {
	if testing.Short() {
		t.Skip("plays six seconds in real time")
	}
	cfg := shortConfig(t)
	path := shortCapture(t)

	live, liveStats := runLeg(t, ingest.NewReplaySource(path, 1), cfg, false)
	replayed, replayStats := runLeg(t, ingest.NewReplaySource(path, 0), cfg, true)

	if !reflect.DeepEqual(live, replayed) || liveStats != replayStats {
		t.Fatalf("replay diverged from the live program at 1×\n live:   %+v %+v\n replay: %+v %+v",
			live, liveStats, replayed, replayStats)
	}
	// The capture has to have exercised what it claims, or agreement is vacuous.
	kinds := map[string]bool{}
	for _, f := range replayed {
		for _, k := range f.Kinds {
			kinds[k] = true
		}
		if strings.Contains(f.Pattern, "Traceback") && !strings.Contains(f.Pattern, "ValueError") {
			t.Errorf("the traceback was not coalesced: %q", f.Pattern)
		}
	}
	for _, k := range []string{"novel:first", "novel:cooloff", "burst", "level FATAL"} {
		if !kinds[k] {
			t.Errorf("no escalation carried %q: %+v", k, replayed)
		}
	}
	if replayStats.Suppressed == 0 {
		t.Errorf("the rate limiter never bit: %+v", replayStats)
	}
	t.Logf("both legs: %d escalations, %+v", len(replayed), replayStats)
}

// TestWallClockAtMaxSpeedDiverges shows the comparison above can fail: the live program fed
// the committed capture at max speed — what piping a capture into it amounts to, but through
// the journald decode — must NOT agree with replay. The count it produces is logged, not
// asserted: it is a measurement, and the BACKLOG entry for #35 records it.
func TestWallClockAtMaxSpeedDiverges(t *testing.T) {
	const capture = "testdata/replay/basic.jsonl"
	cfg := score.Defaults()
	wall, wallStats := runLeg(t, ingest.NewReplaySource(capture, 0), cfg, false)
	replayed, replayStats := runLeg(t, ingest.NewReplaySource(capture, 0), cfg, true)
	t.Logf("wall clock at max speed: %d escalations %+v %+v", len(wall), wallStats, wall)
	t.Logf("replay clock:            %d escalations %+v", len(replayed), replayStats)
	if reflect.DeepEqual(wall, replayed) {
		t.Fatal("the wall clock at max speed agreed with replay: the comparison cannot tell them apart")
	}
}

// TestReplaySpeedDoesNotChangeTheAnswer: speed is pacing, not an input. The committed
// capture replayed at max and at 1000× flags the same things.
func TestReplaySpeedDoesNotChangeTheAnswer(t *testing.T) {
	const capture = "testdata/replay/basic.jsonl"
	cfg := score.Defaults()
	fast, fastStats := runLeg(t, ingest.NewReplaySource(capture, 0), cfg, true)
	paced, pacedStats := runLeg(t, ingest.NewReplaySource(capture, 1000), cfg, true)
	if !reflect.DeepEqual(fast, paced) || fastStats != pacedStats {
		t.Fatalf("speed changed the answer:\n max:   %+v %+v\n 1000×: %+v %+v", fast, fastStats, paced, pacedStats)
	}
}
