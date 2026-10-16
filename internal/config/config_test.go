// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/anonymize"
	"github.com/maxie7/logscry/internal/pipeline"
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

// TestGroupTimeout covers the multi-line coalescing knob: a sensible default, a flag
// override, a rejected negative, and zero (which is allowed and disables grouping).
func TestGroupTimeout(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Group.Timeout != 200*time.Millisecond {
		t.Errorf("default group timeout = %v, want 200ms", cfg.Group.Timeout)
	}

	cfg, err = Load([]string{"--group-timeout", "500ms"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Group.Timeout != 500*time.Millisecond {
		t.Errorf("group timeout = %v, want 500ms from the flag", cfg.Group.Timeout)
	}

	if _, err := Load([]string{"--group-timeout", "-1s"}); err == nil {
		t.Error("a negative group timeout was accepted")
	}
	if _, err := Load([]string{"--group-timeout", "0"}); err != nil {
		t.Errorf("zero group timeout (disable) was rejected: %v", err)
	}
}

// TestAnonymizeFlag: --llm-anonymize is bound and off by default, and its repeatable
// suffix flag appends like the other list flags.
func TestAnonymizeFlag(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Anonymize {
		t.Error("anonymization is on by default; it must be opt-in")
	}

	cfg, err = Load([]string{"--llm-anonymize", "--llm-anonymize-suffix", "acmecloud", "--llm-anonymize-suffix", "corp1"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LLM.Anonymize {
		t.Error("--llm-anonymize did not enable masking")
	}
	if len(cfg.LLM.AnonymizeSuffixes) != 2 || cfg.LLM.AnonymizeSuffixes[0] != "acmecloud" {
		t.Errorf("AnonymizeSuffixes = %v, want both repeats", cfg.LLM.AnonymizeSuffixes)
	}
}

// TestAnonymizeSuffixNormalised: a configured suffix is read without its dots. The README
// writes the built-ins as `.internal`, so `--llm-anonymize-suffix .acme` is the natural
// spelling, and it used to be spliced into the bare-host rule verbatim: `\.acme` needs
// "db..acme" and never matched, so every host on it was sent on both paths. A trailing dot
// matched only before a word character. Both are stripped from the RESOLVED value, so the
// config file is held to the same rule as the flag. Case is left alone: the rule is (?i).
func TestAnonymizeSuffixNormalised(t *testing.T) {
	want := []string{"acme", "acme", "acme", "eu1.acme", "Acme"}
	cfg, err := Load([]string{"--llm-anonymize",
		"--llm-anonymize-suffix", ".acme", "--llm-anonymize-suffix", "acme.",
		"--llm-anonymize-suffix", "..acme..", "--llm-anonymize-suffix", ".eu1.acme",
		"--llm-anonymize-suffix", "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.LLM.AnonymizeSuffixes, want) {
		t.Errorf("flag: AnonymizeSuffixes = %q, want %q", cfg.LLM.AnonymizeSuffixes, want)
	}

	path := writeConfig(t, "llm:\n  anonymize: true\n  anonymize_suffixes: [\".acme\", \"acme.\"]\n")
	if cfg, err = Load([]string{"--config", path}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"acme", "acme"}; !slices.Equal(cfg.LLM.AnonymizeSuffixes, want) {
		t.Errorf("file: AnonymizeSuffixes = %q, want %q", cfg.LLM.AnonymizeSuffixes, want)
	}
}

// TestAnonymizeSuffixEmptyRejected: a suffix that is empty, or empty once its dots are gone,
// is refused at load with an error naming the flag. Spliced in, it was an empty alternative,
// and the bare-host rule then masked every dotted prefix before a word character:
// "java.lang.NullPointerException" reached the model as "<HOST_1>NullPointerException".
func TestAnonymizeSuffixEmptyRejected(t *testing.T) {
	for _, v := range []string{"", ".", ".."} {
		_, err := Load([]string{"--llm-anonymize", "--llm-anonymize-suffix", "acme", "--llm-anonymize-suffix", v})
		if err == nil || !strings.Contains(err.Error(), "--llm-anonymize-suffix") {
			t.Errorf("flag %q: err = %v, want a rejection naming --llm-anonymize-suffix", v, err)
		}
	}
	path := writeConfig(t, "llm:\n  anonymize_suffixes: [\"acme\", \"\"]\n")
	if _, err := Load([]string{"--config", path}); err == nil || !strings.Contains(err.Error(), "--llm-anonymize-suffix") {
		t.Errorf("file: err = %v, want a rejection naming --llm-anonymize-suffix", err)
	}
}

// TestAnonymizeSuffixReachesTheMasker runs the two components back to back: the suffix as
// config resolves it is the one the anonymizer is built from (cmd/logscry passes
// cfg.LLM.AnonymizeSuffixes to it unchanged), and a host on it is masked on the raw line and
// on the pre-masked template.
func TestAnonymizeSuffixReachesTheMasker(t *testing.T) {
	cfg, err := Load([]string{"--llm-anonymize", "--llm-anonymize-suffix", ".acme"})
	if err != nil {
		t.Fatal(err)
	}
	const raw = "peer db.acme down"
	tmpl, _ := pipeline.Templatize(raw)
	for _, in := range []string{raw, tmpl} {
		got, err := anonymize.New(cfg.LLM.AnonymizeSuffixes...).Mask(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != "peer <HOST_1> down" {
			t.Errorf("host on a dotted suffix not masked:\n in:   %q\n got:  %q\n want: %q", in, got, "peer <HOST_1> down")
		}
	}
}

// TestStreamFlag: --llm-stream is bound and OFF by default. The default is the point —
// provider behaviour around stream + response_format varies, so the release that introduces
// it must not change what anyone's explanations look like unless they ask.
func TestStreamFlag(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Stream {
		t.Error("streaming is on by default; it must be opt-in")
	}

	cfg, err = Load([]string{"--llm-stream"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LLM.Stream {
		t.Error("--llm-stream did not enable streaming")
	}
}

// TestVersionFlag: --version is bound and parsed like any other flag, and it short-circuits
// before the config file is read or validated — so it works even with a broken --config.
func TestVersionFlag(t *testing.T) {
	cfg, err := Load([]string{"--version"})
	if err != nil {
		t.Fatalf("--version returned an error: %v", err)
	}
	if !cfg.Version {
		t.Error("cfg.Version = false, want true from --version")
	}

	// The short-circuit must precede loadFile: a nonexistent config is not an error here.
	cfg, err = Load([]string{"--version", "--config", "/nonexistent.yaml"})
	if err != nil {
		t.Fatalf("--version with a missing config errored: %v", err)
	}
	if !cfg.Version {
		t.Error("cfg.Version = false, want true even with a bad --config")
	}
}

// TestJournaldFlags: the source is off by default, its flags bind, and the priority
// floor is validated — an out-of-range -p is a journalctl invocation that fails at
// startup, which is better caught next to the flag that set it.
func TestJournaldFlags(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Journald.Enabled || len(cfg.Journald.Units) > 0 {
		t.Errorf("journald is on by default: %+v", cfg.Journald)
	}
	if cfg.Journald.Priority != 7 {
		t.Errorf("default priority = %d, want 7 (everything)", cfg.Journald.Priority)
	}

	cfg, err = Load([]string{"--journald", "--journald-priority", "4",
		"--journald-unit", "nginx", "--journald-unit", "sshd"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Journald.Enabled {
		t.Error("--journald did not enable the source")
	}
	if cfg.Journald.Priority != 4 {
		t.Errorf("priority = %d, want 4", cfg.Journald.Priority)
	}
	if want := []string{"nginx", "sshd"}; !slices.Equal(cfg.Journald.Units, want) {
		t.Errorf("units = %q, want %q", cfg.Journald.Units, want)
	}

	for _, bad := range []string{"8", "-1"} {
		if _, err := Load([]string{"--journald-priority", bad}); err == nil {
			t.Errorf("priority %s was accepted, want a range error", bad)
		}
	}
	for _, ok := range []string{"0", "7"} {
		if _, err := Load([]string{"--journald-priority", ok}); err != nil {
			t.Errorf("priority %s was rejected: %v", ok, err)
		}
	}
}

// TestJournaldFromFile: the block is reachable from logscry.yaml like every other
// section, including the repeatable unit list.
func TestJournaldFromFile(t *testing.T) {
	path := writeConfig(t, "journald:\n  enabled: true\n  priority: 3\n  unit:\n    - nginx\n")
	cfg, err := Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Journald.Enabled || cfg.Journald.Priority != 3 {
		t.Errorf("journald = %+v, want enabled at priority 3", cfg.Journald)
	}
	if want := []string{"nginx"}; !slices.Equal(cfg.Journald.Units, want) {
		t.Errorf("units = %q, want %q", cfg.Journald.Units, want)
	}
}
