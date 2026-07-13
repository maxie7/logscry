// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/score"
)

// writeConfig drops a YAML document in a temp dir and returns its path.
func writeConfig(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logscry.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Score != score.Defaults() {
		t.Errorf("Score = %+v, want the tuned-quiet defaults", cfg.Score)
	}
	if cfg.Plain || cfg.ExplainDryRun {
		t.Error("plain / dry-run are on by default")
	}
}

// TestPrecedence is the whole point of the loader: a flag the user typed beats the
// file, the file beats the defaults, and — the subtle one — a flag the user did NOT
// type must not silently overwrite the file with its own default value.
func TestPrecedence(t *testing.T) {
	path := writeConfig(t, `
score:
  threshold: 2.5
  cooloff: 45m
  rate_limit: 3
`)

	cfg, err := Load([]string{"--config", path, "--threshold", "4"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Score.Threshold != 4 {
		t.Errorf("Threshold = %v, want 4 (the flag beats the file)", cfg.Score.Threshold)
	}
	if cfg.Score.Cooloff != 45*time.Minute {
		t.Errorf("Cooloff = %v, want 45m (the file beats the default)", cfg.Score.Cooloff)
	}
	if cfg.Score.RatePerMin != 3 {
		t.Errorf("RatePerMin = %d, want 3: an untyped flag's default overwrote the file", cfg.Score.RatePerMin)
	}
	if cfg.Score.BurstMinCount != score.Defaults().BurstMinCount {
		t.Errorf("BurstMinCount = %d, want the default: nothing set it", cfg.Score.BurstMinCount)
	}
}

// TestUnknownKeyIsAnError: a typo in a tuning file must not leave the tool quietly on a
// default the user believes they changed.
func TestUnknownKeyIsAnError(t *testing.T) {
	path := writeConfig(t, "score:\n  thresold: 2.0\n")
	if _, err := Load([]string{"--config", path}); err == nil {
		t.Error("Load accepted a misspelled config key")
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	if _, err := Load([]string{"--threshold", "0"}); err == nil {
		t.Error("Load accepted a zero threshold, which would escalate everything")
	}
	if _, err := Load([]string{"--burst-min-count", "100000"}); err == nil {
		t.Error("Load accepted a burst volume gate that could never be reached")
	}
	if _, err := Load([]string{"--burst-multiplier", "1"}); err == nil {
		t.Error("Load accepted a 1x burst multiplier, which would fire on steady traffic")
	}
}

// TestFlagsAndArgv checks the surface M1/M2 already relied on, now that it lives here:
// the "--" split, the repeatable label flag, and the Docker selectors.
func TestFlagsAndArgv(t *testing.T) {
	cfg, err := Load([]string{
		"--plain", "--explain-dry-run",
		"--docker-all", "--docker-label", "app=api", "--docker-label", "env=prod",
		"--", "./myapp", "--verbose",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.Plain || !cfg.ExplainDryRun || !cfg.Docker.All {
		t.Errorf("flags not applied: %+v", cfg)
	}
	if len(cfg.Docker.Labels) != 2 || cfg.Docker.Labels[0] != "app=api" {
		t.Errorf("Labels = %v, want both repeats", cfg.Docker.Labels)
	}
	if len(cfg.Argv) != 2 || cfg.Argv[0] != "./myapp" || cfg.Argv[1] != "--verbose" {
		t.Errorf("Argv = %v, want everything after the -- separator", cfg.Argv)
	}
	if cfg.Docker.Tail != "100" {
		t.Errorf("Tail = %q, want the default 100", cfg.Docker.Tail)
	}
}

// TestAPIKeyComesFromEnvOnly: the one secret is env-only, so it cannot end up in a
// shell history or a committed config file.
func TestAPIKeyComesFromEnvOnly(t *testing.T) {
	t.Setenv(apiKeyEnv, "sk-secret")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "sk-secret" {
		t.Errorf("APIKey = %q, want the value from %s", cfg.LLM.APIKey, apiKeyEnv)
	}

	path := writeConfig(t, "llm:\n  api_key: from-the-file\n")
	if _, err := Load([]string{"--config", path}); err == nil {
		t.Error("the config file was allowed to carry an api_key")
	}
}
