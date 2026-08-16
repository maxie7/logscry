// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/export"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// exportTo opens a JSONL writer on a temp file and returns it with a func that closes it and
// reads the records back.
func exportTo(t *testing.T) (*export.Writer, func() []map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anomalies.jsonl")
	w, err := export.Open(path)
	if err != nil {
		t.Fatalf("export.Open: %v", err)
	}
	return w, func() []map[string]any {
		if err := w.Close(); err != nil {
			t.Fatalf("export close: %v", err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read export: %v", err)
		}
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("exported line does not parse: %v\n%s", err, line)
			}
			out = append(out, rec)
		}
		return out
	}
}

// TestExportRecordsADroppedEscalation: a full escalation queue means the model is slower
// than the anomalies, so nothing is ever coming for this one — it is terminal the moment it
// fires. It is still an anomaly and still belongs in the file, with the reason on it.
func TestExportRecordsADroppedEscalation(t *testing.T) {
	exp, read := exportTo(t)

	// Capacity zero with no reader: the scorer's non-blocking send cannot land.
	escalations := make(chan score.EscalationRequest)
	p := New(score.New(escalateAlways(), escalations))
	p.explain = true
	p.export = exp

	ev := p.Process(model.LogLine{Source: "proc:app", Stream: model.Stderr,
		Raw: "PANIC: nil map write in handler 42"}, time.Now())
	if !ev.Escalate || ev.Queued {
		t.Fatalf("setup: escalate=%v queued=%v, want an escalation that could not be queued", ev.Escalate, ev.Queued)
	}

	recs := read()
	if len(recs) != 1 {
		t.Fatalf("wrote %d records, want 1", len(recs))
	}
	if recs[0]["kind"] != export.KindAnomaly {
		t.Errorf("kind = %v, want %s", recs[0]["kind"], export.KindAnomaly)
	}
	ex := recs[0]["explanation"].(map[string]any)
	if ex["state"] != "unavailable" {
		t.Errorf("state = %v, want unavailable", ex["state"])
	}
	if !strings.Contains(ex["error"].(string), "queue full") {
		t.Errorf("error = %v, want it to name the full queue", ex["error"])
	}
}

// TestExportWithoutAnLLMStageWritesWouldEscalate is --explain-dry-run's half of the file.
// No model is attached, so nothing will ever explain these; the flag is terminal as it
// fires, and the record says so rather than sitting unwritten forever.
//
// It also pins the branch order: with a nil escalation channel the scorer reports Queued
// false — for a completely different reason than a full queue — and that must NOT be
// recorded as a dropped escalation.
func TestExportWithoutAnLLMStageWritesWouldEscalate(t *testing.T) {
	exp, read := exportTo(t)

	p := New(score.New(escalateAlways(), nil)) // no LLM stage at all, as in dry-run
	p.export = exp

	ev := p.Process(model.LogLine{Source: "proc:app", Stream: model.Stderr,
		Raw: "PANIC: nil map write in handler 42"}, time.Now())
	if !ev.Escalate {
		t.Fatal("setup: the line did not escalate")
	}

	recs := read()
	if len(recs) != 1 {
		t.Fatalf("wrote %d records, want 1", len(recs))
	}
	if recs[0]["kind"] != export.KindWouldEscalate {
		t.Errorf("kind = %v, want %s", recs[0]["kind"], export.KindWouldEscalate)
	}
	ex := recs[0]["explanation"].(map[string]any)
	if ex["state"] != "not_requested" {
		t.Errorf("state = %v, want not_requested", ex["state"])
	}
	if ex["error"] != "" {
		t.Errorf("error = %q, want empty — no escalation was dropped, there was nowhere to send one", ex["error"])
	}
	if recs[0]["score"] == nil || recs[0]["reasons"] == nil {
		t.Error("a would-escalate record without a score or reasons is useless for calibration")
	}
}

// TestExportWritesOneLinePerStreamedAnswer is the streaming guard at the wiring level: in
// the TUI the pool's answers are routed through this goroutine, so every progressive update
// passes through attach. Exactly one of them may reach the file.
func TestExportWritesOneLinePerStreamedAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exp, read := exportTo(t)
	lines := make(chan model.LogLine, 4)
	snaps := make(chan Snapshot, 1)
	escalations := make(chan score.EscalationRequest, 4)
	explanations := make(chan model.Explanation, 4)

	go Run(ctx, lines, Options{
		Snapshots:    snaps,
		Interval:     time.Millisecond,
		Scorer:       score.New(escalateAlways(), escalations),
		Escalations:  escalations,
		Explanations: explanations,
		Export:       exp,
	})

	lines <- model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}
	req := <-escalations

	partial := model.Explanation{Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainPending,
		Summary: "A handler wrote to a nil map."}
	explanations <- partial
	partial.LikelyCause = "The cache map is never initialised."
	explanations <- partial
	explanations <- model.Explanation{Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainDone,
		Summary: "A handler wrote to a nil map.", LikelyCause: "The cache map is never initialised.",
		Suggestion: "Make the map in NewServer."}

	waitFor(t, snaps, "the terminal explanation to land", func(s Snapshot) bool {
		return len(s.Escalations) == 1 && s.Escalations[0].Explanation != nil &&
			s.Escalations[0].Explanation.State == model.ExplainDone
	})

	recs := read()
	if len(recs) != 1 {
		t.Fatalf("wrote %d records for one anomaly, want 1 — partials must not reach the file", len(recs))
	}
	ex := recs[0]["explanation"].(map[string]any)
	if ex["suggestion"] != "Make the map in NewServer." {
		t.Errorf("the record is not the terminal answer: %#v", ex)
	}
}

// TestExportIsIdenticalForBothRenderers is the guarantee that the file is a property of the
// run and not of what was watching it. The TUI routes explanations through this goroutine
// and could therefore read a fresher count off live template state; --plain taps them on its
// own goroutine and could not. If either took what was locally convenient, the same input
// would produce two different files depending on whether a terminal was attached.
//
// So both capture at the flag instant, and this compares the records byte for byte with only
// the timestamps normalized — those are wall-clock and genuinely differ between runs.
func TestExportIsIdenticalForBothRenderers(t *testing.T) {
	line := model.LogLine{Source: "proc:app", Stream: model.Stderr, Raw: "PANIC: nil map write in handler 42"}

	// The TUI shape: explanations are delivered to the pipeline, which attaches them.
	viaPipeline := func() map[string]any {
		exp, read := exportTo(t)
		escalations := make(chan score.EscalationRequest, 4)
		p := New(score.New(escalateAlways(), escalations))
		p.explain = true
		p.export = exp

		p.Process(line, time.Now())
		req := <-escalations
		p.attach(model.Explanation{Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainDone,
			Summary: "A handler wrote to a nil map.", At: time.Now()})

		recs := read()
		if len(recs) != 1 {
			t.Fatalf("TUI path wrote %d records, want 1", len(recs))
		}
		return recs[0]
	}

	// The --plain shape: the pipeline never sees the explanation; the consumer resolves it.
	viaConsumer := func() map[string]any {
		exp, read := exportTo(t)
		escalations := make(chan score.EscalationRequest, 4)
		p := New(score.New(escalateAlways(), escalations))
		p.explain = true
		p.export = exp

		p.Process(line, time.Now())
		req := <-escalations
		exp.Resolve(model.Explanation{Hash: req.Hash, Pattern: req.Pattern, State: model.ExplainDone,
			Summary: "A handler wrote to a nil map.", At: time.Now()})

		recs := read()
		if len(recs) != 1 {
			t.Fatalf("plain path wrote %d records, want 1", len(recs))
		}
		return recs[0]
	}

	tui, plain := normalizeTimes(viaPipeline()), normalizeTimes(viaConsumer())
	gotTUI, err := json.Marshal(tui)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotPlain, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(gotTUI) != string(gotPlain) {
		t.Errorf("the two renderers produced different records for the same input:\nTUI:   %s\nplain: %s", gotTUI, gotPlain)
	}
}

// normalizeTimes replaces the wall-clock fields with a placeholder, and fails loudly if one
// is missing — a comparison that silently ignored an absent key would pass for the wrong
// reason.
func normalizeTimes(rec map[string]any) map[string]any {
	for _, key := range []string{"first_seen", "last_seen_at_flag"} {
		if _, ok := rec[key]; ok {
			rec[key] = "<time>"
		}
	}
	if ex, ok := rec["explanation"].(map[string]any); ok {
		if _, ok := ex["at"]; ok {
			ex["at"] = "<time>"
		}
	}
	return rec
}

// TestExportOffChangesNothing: with no --export the writer is nil and every call site runs
// unconditionally through it, so the default path has to be exactly the old one — including
// the explanation states, which is what the TUI renders.
func TestExportOffChangesNothing(t *testing.T) {
	escalations := make(chan score.EscalationRequest, 4)
	p := New(score.New(escalateAlways(), escalations))
	p.explain = true // no p.export: --export was not given

	ev := p.Process(model.LogLine{Source: "proc:app", Stream: model.Stderr,
		Raw: "PANIC: nil map write in handler 42"}, time.Now())
	if !ev.Escalate || !ev.Queued {
		t.Fatalf("escalate=%v queued=%v, want a queued escalation", ev.Escalate, ev.Queued)
	}
	tmpl := p.templates[ev.Hash]
	if tmpl.Explanation == nil || tmpl.Explanation.State != model.ExplainPending {
		t.Fatalf("explanation = %#v, want the pending state the card renders", tmpl.Explanation)
	}
	if p.explaining != 1 {
		t.Errorf("explaining = %d, want 1", p.explaining)
	}
}

// TestExportStaysFrozenWhileTheCardMoves asserts the two deliberately OPPOSITE semantics
// side by side, which is the only reason they are one test.
//
// An export record describes a decision at a moment: count_at_flag and last_seen_at_flag
// are named for what they are and must never move once written, so a second flag of the
// same template writes a SECOND record rather than editing the first. A card describes a
// template now: one card, live count, and a flag history saying it has fired twice.
//
// Getting either half wrong is invisible from the other, and issue #34 was exactly the
// card half having quietly adopted the export's semantics.
func TestExportStaysFrozenWhileTheCardMoves(t *testing.T) {
	exp, read := exportTo(t)
	r := newCardRun(64)
	r.p.export = exp

	r.mustEscalate(t, escalatingLine, cardBase)
	for i := 1; i < 5; i++ {
		r.feed(escalatingLine, cardBase.Add(time.Duration(i)*2*time.Second))
	}
	back := cardBase.Add(61 * time.Minute)
	r.mustEscalate(t, escalatingLine, back)

	// The card: one of it, live.
	snap := r.snap(back)
	if len(snap.Escalations) != 1 {
		t.Fatalf("got %d cards for one template, want 1", len(snap.Escalations))
	}
	card := snap.Escalations[0]
	if card.Count != 6 || card.FlagCount != 2 {
		t.Errorf("card = x%d / %d flags, want x6 / 2 flags", card.Count, card.FlagCount)
	}
	if !card.LastSeen.Equal(back) {
		t.Errorf("card LastSeen = %s, want the live %s",
			card.LastSeen.Format(time.TimeOnly), back.Format(time.TimeOnly))
	}

	// The file: two records, each frozen at its own flag.
	recs := read()
	if len(recs) != 2 {
		t.Fatalf("wrote %d records for two flags, want 2 — a card merging must not merge the file",
			len(recs))
	}
	wantCounts := []float64{1, 6} // JSON numbers decode as float64
	wantLastSeen := []time.Time{cardBase, back}
	for i, rec := range recs {
		if got := rec["count_at_flag"]; got != wantCounts[i] {
			t.Errorf("record %d count_at_flag = %v, want %v", i, got, wantCounts[i])
		}
		got, err := time.Parse(time.RFC3339Nano, rec["last_seen_at_flag"].(string))
		if err != nil {
			t.Fatalf("record %d last_seen_at_flag does not parse: %v", i, err)
		}
		if !got.Equal(wantLastSeen[i]) {
			t.Errorf("record %d last_seen_at_flag = %s, want %s — an export record never moves",
				i, got.Format(time.TimeOnly), wantLastSeen[i].Format(time.TimeOnly))
		}
	}
	// And the first record must NOT have been dragged up to the card's live numbers.
	if recs[0]["count_at_flag"] == recs[1]["count_at_flag"] {
		t.Error("both records carry the same count_at_flag: the export followed the card")
	}
}
