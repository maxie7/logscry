// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/maxie7/logscry/internal/config"
	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// TestDryRunBuildsNoLLMStage is the guarantee behind --explain-dry-run, and it is
// structural rather than behavioural on purpose. The mode's promise is not "we remember
// to skip the call": it is that the process contains no backend and no worker pool at
// all, so there is nothing that COULD call a model. A nil escalation channel is what that
// looks like from here — and the scorer, handed nil, emits nowhere (see score.emit).
//
// The nil is load-bearing for a second reason: a real channel with no reader would fill
// after a handful of escalations and count every one after that as a drop, corrupting the
// very numbers dry-run exists to calibrate.
func TestDryRunBuildsNoLLMStage(t *testing.T) {
	cfg := config.Defaults()
	cfg.ExplainDryRun = true

	escalations, explanations := startLLM(context.Background(), cfg)
	if escalations != nil {
		t.Error("dry run created an escalation channel: something is listening for work")
	}
	if explanations != nil {
		t.Error("dry run created an explanation channel: a worker pool is running")
	}
}

// TestLLMStageIsWiredByDefault: the other half of the same fact — a normal run does get a
// pool, so that "no requests were made" in dry-run means something.
func TestLLMStageIsWiredByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the pool this starts

	escalations, explanations := startLLM(ctx, config.Defaults())
	if escalations == nil || explanations == nil {
		t.Fatal("a normal run built no LLM stage: nothing would ever be explained")
	}
	// Room to absorb a burst without blocking the scorer, which must never wait on a model.
	if cap(escalations) == 0 {
		t.Error("the escalation channel is unbuffered: the scorer would drop every escalation")
	}
}

// TestPlainEscalationLines: --plain is the CI and pipe mode, so its output is an
// interface. The dry-run wording is what a calibration run greps for; a dropped
// escalation must not masquerade as one that is being worked on.
func TestPlainEscalationLines(t *testing.T) {
	ev := pipeline.Event{
		Pattern:  "panic: nil map write in handler <NUM>",
		Escalate: true,
		Queued:   true,
		Score:    2,
		Reasons:  []string{"novel template (first seen)", "level PANIC"},
	}

	if got := escalated(ev, true); !strings.HasPrefix(got, "WOULD ESCALATE:") {
		t.Errorf("dry-run line = %q, want it to start with WOULD ESCALATE", got)
	}
	if got := escalated(ev, false); !strings.HasPrefix(got, "ESCALATED:") {
		t.Errorf("live line = %q, want it to start with ESCALATED", got)
	}

	ev.Queued = false // the queue was full: nothing is coming for this one
	got := escalated(ev, false)
	if !strings.Contains(got, "DROPPED") {
		t.Errorf("dropped escalation line = %q, want it to say the escalation was dropped", got)
	}
}

// TestPlainExplanationLines: the answer arrives seconds after the escalation scrolled
// past, so a line-oriented consumer needs the template repeated to know what it is about.
func TestPlainExplanationLines(t *testing.T) {
	got := explained(model.Explanation{
		Pattern:     "panic: nil map write in handler <NUM>",
		State:       model.ExplainDone,
		Summary:     "A handler wrote to a nil map.",
		LikelyCause: "The cache map is never initialised.",
		Suggestion:  "Make the map in NewServer.",
	})
	for _, want := range []string{
		"EXPLAINED: panic: nil map write in handler <NUM>",
		"what:  A handler wrote to a nil map.",
		"cause: The cache map is never initialised.",
		"check: Make the map in NewServer.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation output is missing %q:\n%s", want, got)
		}
	}

	failed := explained(model.Explanation{
		Pattern: "panic: nil map write in handler <NUM>",
		State:   model.ExplainFailed,
		Err:     "HTTP 500 (server error): model not loaded",
	})
	if !strings.Contains(failed, "EXPLANATION UNAVAILABLE") || !strings.Contains(failed, "model not loaded") {
		t.Errorf("failed explanation output = %q, want it to say what went wrong", failed)
	}
}

// sourceNames is the observable shape of a source set: which sources were built, in
// order. Names are what the Source contract exposes, and they are what lines get tagged
// with, so asserting on them tests the thing the user sees.
func sourceNames(srcs []ingest.Source) []string {
	names := make([]string, len(srcs))
	for i, s := range srcs {
		names[i] = s.Name()
	}
	return names
}

// TestSourcesJournald: the new flag composes the way Docker's do rather than inventing a
// mode. Naming a unit is enough on its own (as --docker-name is), tuning the priority is
// not (as --docker-tail is not), and journald stacks with a subprocess the way Docker
// already does — ingest.Run fans them into one channel.
func TestSourcesJournald(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		want          []string
		wantStdinOnly bool
	}{
		{"no flags is unchanged: stdin only", nil, []string{"stdin"}, true},
		{"--journald selects the journal", []string{"--journald"}, []string{"journald"}, false},
		{
			"a unit implies the source",
			[]string{"--journald-unit", "nginx"},
			[]string{"journald"}, false,
		},
		{
			"a priority floor alone does not",
			[]string{"--journald-priority", "3"},
			[]string{"stdin"}, true,
		},
		{
			"it composes with a subprocess",
			[]string{"--journald", "--", "./myapp"},
			[]string{"proc:myapp", "journald"}, false,
		},
		{
			"and with Docker",
			[]string{"--journald", "--docker-all"},
			[]string{"docker", "journald"}, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(tc.args)
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			got, stdinOnly := sources(cfg)
			if !slices.Equal(sourceNames(got), tc.want) {
				t.Errorf("sources = %q, want %q", sourceNames(got), tc.want)
			}
			if stdinOnly != tc.wantStdinOnly {
				t.Errorf("stdinOnly = %v, want %v", stdinOnly, tc.wantStdinOnly)
			}
		})
	}
}

// TestJournaldSelectorPassesThrough: the unit filter and priority floor have to reach
// the source, or the flags are decoration.
func TestJournaldSelectorPassesThrough(t *testing.T) {
	cfg, err := config.Load([]string{"--journald-unit", "nginx", "--journald-unit", "sshd",
		"--journald-priority", "4"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srcs, _ := sources(cfg)
	if len(srcs) != 1 {
		t.Fatalf("built %d sources, want 1", len(srcs))
	}
	js, ok := srcs[0].(*ingest.JournaldSource)
	if !ok {
		t.Fatalf("source is %T, want *ingest.JournaldSource", srcs[0])
	}
	if want := []string{"nginx", "sshd"}; !slices.Equal(js.Units, want) {
		t.Errorf("Units = %q, want %q", js.Units, want)
	}
	if js.Priority != 4 {
		t.Errorf("Priority = %d, want 4", js.Priority)
	}
}

// TestJournaldNoticeOnlyWhenJournaldIsOn: the reduced-view warning is about a source
// that is running. Without the flag there is nothing to warn about, and a notice on a
// Docker-only run would be pure noise.
func TestJournaldNoticeOnlyWhenJournaldIsOn(t *testing.T) {
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if notice := journaldNotice(cfg); notice != nil {
		t.Errorf("journaldNotice without --journald = %v, want nil", notice)
	}
}
