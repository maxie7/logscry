// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// TestReplayRequiresDryRun: replay is a calibration instrument. It runs the scorer, never a
// model — so it runs only in the mode that builds no backend, and says so in one line that
// names both flags.
func TestReplayRequiresDryRun(t *testing.T) {
	_, err := Load([]string{"--replay", "capture.jsonl"})
	if err == nil {
		t.Fatal("--replay without --explain-dry-run was accepted")
	}
	for _, want := range []string{"--replay requires --explain-dry-run", "replay runs the scorer, not a model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

// TestReplayDryRunIsTheResolvedValue: the check reads the RESOLVED configuration — the same
// field the LLM stage is (not) built from — never the flag alone. So a user who enabled
// dry-run in their config file is not told to pass a flag, and a flag that turns it back
// off is refused. A guard that consulted a different object from the mechanism would agree
// with it only until a second path set the value.
func TestReplayDryRunIsTheResolvedValue(t *testing.T) {
	file := writeConfig(t, "explain_dry_run: true\n")

	cfg, err := Load([]string{"--config", file, "--replay", "capture.jsonl"})
	if err != nil {
		t.Fatalf("dry-run set in the config file did not satisfy --replay: %v", err)
	}
	if !cfg.ExplainDryRun || cfg.Replay != "capture.jsonl" {
		t.Fatalf("resolved ExplainDryRun=%v Replay=%q", cfg.ExplainDryRun, cfg.Replay)
	}

	if _, err := Load([]string{"--config", file, "--explain-dry-run=false", "--replay", "capture.jsonl"}); err == nil {
		t.Fatal("a flag turning dry-run off over the file was accepted for a replay")
	}
}

// TestReplayIsTheOnlySource: a replay is one ordered capture on its own clock. Mixing it with
// a live source would put two clocks into one pipeline, so any combination is refused.
func TestReplayIsTheOnlySource(t *testing.T) {
	base := []string{"--replay", "capture.jsonl", "--explain-dry-run"}
	for name, extra := range map[string][]string{
		"docker":         {"--docker-all"},
		"docker name":    {"--docker-name", "api"},
		"journald":       {"--journald"},
		"journald unit":  {"--journald-unit", "sshd"},
		"subprocess":     {"--", "./app"},
		"docker label":   {"--docker-label", "a=b"},
		"all of them":    {"--docker-all", "--journald"},
		"subprocess arg": {"--", "sh", "-c", "echo hi"},
	} {
		if _, err := Load(append(append([]string{}, base...), extra...)); err == nil || !strings.Contains(err.Error(), "--replay") {
			t.Errorf("%s: err = %v, want a refusal naming --replay", name, err)
		}
	}
	if _, err := Load(base); err != nil {
		t.Fatalf("replay alone was refused: %v", err)
	}
}

// TestReplaySpeed: max (the default) means no pacing; a number, with or without an x, is a
// multiple of real time. Zero, negatives and words are refused rather than guessed at.
func TestReplaySpeed(t *testing.T) {
	for in, want := range map[string]float64{
		"max": 0, "MAX": 0, "1": 1, "1x": 1, "10x": 10, "0.5": 0.5, "2.5X": 2.5,
	} {
		cfg, err := Load([]string{"--replay", "c.jsonl", "--explain-dry-run", "--replay-speed", in})
		if err != nil {
			t.Errorf("--replay-speed %s: %v", in, err)
			continue
		}
		if cfg.ReplaySpeed != want {
			t.Errorf("--replay-speed %s = %g, want %g", in, cfg.ReplaySpeed, want)
		}
	}
	cfg, err := Load([]string{"--replay", "c.jsonl", "--explain-dry-run"})
	if err != nil || cfg.ReplaySpeed != 0 {
		t.Errorf("default speed = %g (err %v), want 0 (max)", cfg.ReplaySpeed, err)
	}
	for _, bad := range []string{"0", "-1", "0x", "fast", "", "x"} {
		if _, err := Load([]string{"--replay", "c.jsonl", "--explain-dry-run", "--replay-speed", bad}); err == nil {
			t.Errorf("--replay-speed %q was accepted", bad)
		}
	}
}

// TestReplayIsFlagOnly: a config file that silently turned every run into a replay would be
// a trap, so neither key exists in the file.
func TestReplayIsFlagOnly(t *testing.T) {
	for _, doc := range []string{"replay: capture.jsonl\n", "replay_speed: 1\n"} {
		if _, err := Load([]string{"--config", writeConfig(t, doc)}); err == nil {
			t.Errorf("config file key accepted: %q", doc)
		}
	}
}
