// SPDX-License-Identifier: Apache-2.0

// Package config loads logscry configuration from flags, an optional YAML file,
// and the environment (secrets only, e.g. LOGSCRY_API_KEY).
//
// Precedence is defaults < file < explicitly-set flags. "Explicitly set" is the
// subtle part: a flag left alone must not shadow the config file with its own
// default, so the file is applied first and only the flags the user actually typed
// are replayed over it (see Load).
package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/score"
)

// apiKeyEnv is the only secret logscry reads, and it is env-only — never a flag,
// never the config file, so it cannot end up in a shell history or a commit.
const apiKeyEnv = "LOGSCRY_API_KEY"

// Docker selects which containers to follow.
type Docker struct {
	All       bool     `yaml:"all"`
	NameRegex string   `yaml:"name"`
	Labels    []string `yaml:"label"` // k=v, AND-combined
	Tail      string   `yaml:"tail"`  // trailing lines fetched per container on attach
}

// Group tunes multi-line coalescing: continuation lines (stack-trace frames, goroutine
// dumps, indented output) are folded into the preceding logical line before templating,
// so a traceback becomes one event instead of one template per frame.
type Group struct {
	// Timeout is the idle flush. A buffered multi-line event is emitted this long after
	// its last line even if no header follows, so ingestion keeps bounded latency and a
	// partial event is never held forever. Zero disables coalescing (lines pass through).
	Timeout time.Duration `yaml:"timeout"`
}

// Validate rejects a negative timeout. Zero is allowed and means coalescing is off.
func (g Group) Validate() error {
	if g.Timeout < 0 {
		return fmt.Errorf("group timeout must not be negative")
	}
	return nil
}

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Version is set by --version. It short-circuits Load (see below), so it never comes
	// from the file and is never validated — asking for the version must work even when
	// the config is missing or invalid.
	Version bool `yaml:"-"`

	// Sources / mode.
	Plain bool `yaml:"plain"`
	// ExplainDryRun surfaces escalations instead of sending them to a model. It is how
	// the scoring thresholds get calibrated, and it is the CI mode: with it set, no LLM
	// stage is built at all, so no request can be made (see main.startLLM).
	ExplainDryRun bool `yaml:"explain_dry_run"`

	Score  score.Config `yaml:"score"`
	Docker Docker       `yaml:"docker"`
	Group  Group        `yaml:"group"`
	LLM    llm.Config   `yaml:"llm"`

	// Argv is the subprocess to run, i.e. everything after "--". Not configurable
	// from the file: it is positional by nature.
	Argv []string `yaml:"-"`
}

// Defaults returns the zero-config configuration: quiet scoring, no Docker, and a
// local Ollama. Each section owns its own defaults table, so there is one place to tune.
func Defaults() Config {
	return Config{
		Score:  score.Defaults(),
		Docker: Docker{Tail: "100"},
		Group:  Group{Timeout: 200 * time.Millisecond},
		LLM:    llm.Defaults(),
	}
}

// Load resolves configuration from args, an optional --config YAML file, and the
// environment.
func Load(args []string) (Config, error) {
	// Split at the first "--" before flag parsing: the stdlib flag package's own
	// "--" handling is ambiguous with a bare `logscry ./app`, and we require an
	// explicit "--" to treat trailing args as a subprocess.
	flagArgs, argv := splitArgs(args)

	cfg := Defaults()
	fs := flag.NewFlagSet("logscry", flag.ContinueOnError)
	// --config and --version are meta-flags controlling Load's flow rather than runtime
	// tuning, so they are registered here instead of the binder — but on the same flag
	// set, so -h still lists them alongside everything else.
	path := fs.String("config", "", "path to a logscry.yaml config file")
	showVersion := fs.Bool("version", false, "print version and exit")
	b := bind(fs, cfg)

	if err := fs.Parse(flagArgs); err != nil {
		return Config{}, err
	}

	// --version wins before anything can fail: no file is read and nothing is validated,
	// so `logscry --version` reports the version even with a missing or broken config.
	if *showVersion {
		cfg.Version = true
		return cfg, nil
	}

	if *path != "" {
		if err := loadFile(*path, &cfg); err != nil {
			return Config{}, err
		}
	}
	// Replay only the flags the user actually typed, so they win over the file while
	// the ones left alone leave it standing.
	fs.Visit(func(f *flag.Flag) {
		if apply, ok := b[f.Name]; ok {
			apply(&cfg)
		}
	})

	cfg.LLM.APIKey = os.Getenv(apiKeyEnv)
	cfg.Argv = argv

	if err := cfg.Score.Validate(); err != nil {
		return Config{}, fmt.Errorf("score config: %w", err)
	}
	if err := cfg.LLM.Validate(); err != nil {
		return Config{}, fmt.Errorf("llm config: %w", err)
	}
	if err := cfg.Group.Validate(); err != nil {
		return Config{}, fmt.Errorf("group config: %w", err)
	}
	return cfg, nil
}

// loadFile decodes path over cfg, which already holds the defaults: keys absent from
// the document simply keep them. Unknown keys are an error rather than a shrug — a
// typo in a tuning file must not silently leave the tool at a default the user
// believes they changed.
func loadFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	return nil
}

// applyFunc copies one parsed flag value into the resolved config.
type applyFunc func(*Config)

// bind registers every flag against the field it overrides, so that the flag's
// default, its usage text, and the assignment that applies it all live on one line.
// It returns the appliers keyed by flag name, for Load to replay over the file.
//
// def carries the defaults, which double as the flag defaults shown in -h.
func bind(fs *flag.FlagSet, def Config) map[string]applyFunc {
	b := binder{fs: fs, apply: make(map[string]applyFunc)}

	b.boolVar("plain", def.Plain, "plain line output instead of the TUI",
		func(c *Config) *bool { return &c.Plain })
	b.boolVar("explain-dry-run", def.ExplainDryRun,
		"show what would be escalated instead of calling the LLM (threshold calibration)",
		func(c *Config) *bool { return &c.ExplainDryRun })

	// Scoring: the threshold and the weights.
	s := def.Score
	b.floatVar("threshold", s.Threshold, "escalate at or above this score",
		func(c *Config) *float64 { return &c.Score.Threshold })
	b.floatVar("weight-novelty", s.Weights.Novelty, "score weight of a novel template",
		func(c *Config) *float64 { return &c.Score.Weights.Novelty })
	b.floatVar("weight-burst", s.Weights.Burst, "score weight of a burst",
		func(c *Config) *float64 { return &c.Score.Weights.Burst })
	b.floatVar("weight-stderr", s.Weights.Stderr, "score weight of a line on stderr",
		func(c *Config) *float64 { return &c.Score.Weights.Stderr })
	b.floatVar("weight-warn", s.Weights.Warn, "score weight of level WARN",
		func(c *Config) *float64 { return &c.Score.Weights.Warn })
	b.floatVar("weight-error", s.Weights.Error, "score weight of level ERROR/CRITICAL",
		func(c *Config) *float64 { return &c.Score.Weights.Error })
	b.floatVar("weight-fatal", s.Weights.Fatal, "score weight of level FATAL/PANIC",
		func(c *Config) *float64 { return &c.Score.Weights.Fatal })
	b.floatVar("severity-max", s.Weights.SeverityMax, "cap on the summed severity weight",
		func(c *Config) *float64 { return &c.Score.Weights.SeverityMax })

	// Scoring: novelty.
	b.durationVar("cooloff", s.Cooloff, "a template unseen for longer than this is novel again",
		func(c *Config) *time.Duration { return &c.Score.Cooloff })
	b.durationVar("warmup", s.Warmup, "mute novelty for this long at startup",
		func(c *Config) *time.Duration { return &c.Score.Warmup })
	b.intVar("warmup-lines", s.WarmupLines, "mute novelty until this many lines have been seen",
		func(c *Config) *int { return &c.Score.WarmupLines })

	// Scoring: burst.
	b.durationVar("burst-window", s.BurstWindow, "sliding window the burst rate is measured over",
		func(c *Config) *time.Duration { return &c.Score.BurstWindow })
	b.floatVar("burst-multiplier", s.BurstMultiplier, "burst when the rate exceeds this many times the template's own baseline",
		func(c *Config) *float64 { return &c.Score.BurstMultiplier })
	b.intVar("burst-min-count", s.BurstMinCount, "never call fewer than this many occurrences a burst",
		func(c *Config) *int { return &c.Score.BurstMinCount })
	b.durationVar("baseline-min-age", s.BaselineMinAge, "a template has no baseline until it is this old",
		func(c *Config) *time.Duration { return &c.Score.BaselineMinAge })
	b.intVar("baseline-min-count", s.BaselineMinCount, "a template has no baseline until seen this many times",
		func(c *Config) *int { return &c.Score.BaselineMinCount })

	// Scoring: gates and context.
	b.intVar("rate-limit", s.RatePerMin, "global cap on LLM calls per minute",
		func(c *Config) *int { return &c.Score.RatePerMin })
	b.durationVar("cache-ttl", s.CacheTTL, "how long an explained template stays quiet",
		func(c *Config) *time.Duration { return &c.Score.CacheTTL })
	b.intVar("context-lines", s.ContextLines, "recent lines carried with an escalation",
		func(c *Config) *int { return &c.Score.ContextLines })

	// Docker selection.
	b.boolVar("docker-all", def.Docker.All, "follow logs from all Docker containers",
		func(c *Config) *bool { return &c.Docker.All })
	b.stringVar("docker-name", def.Docker.NameRegex, "follow Docker containers whose name matches this regexp",
		func(c *Config) *string { return &c.Docker.NameRegex })
	b.stringVar("docker-tail", def.Docker.Tail, "number of trailing log lines to fetch per container on attach",
		func(c *Config) *string { return &c.Docker.Tail })
	b.listVar("docker-label", "follow Docker containers with this k=v label (repeatable, AND-combined)",
		func(c *Config) *[]string { return &c.Docker.Labels })

	// Multi-line coalescing: fold stack traces / goroutine dumps into one event.
	b.durationVar("group-timeout", def.Group.Timeout,
		"idle flush for multi-line grouping; a buffered stack trace is emitted this long after its last line (0 disables grouping)",
		func(c *Config) *time.Duration { return &c.Group.Timeout })

	// LLM backend and worker pool. The API key is absent on purpose: it is env-only
	// (LOGSCRY_API_KEY), so it cannot end up in a shell history.
	l := def.LLM
	b.stringVar("llm-url", l.BaseURL, "OpenAI-compatible base URL (default: local Ollama)",
		func(c *Config) *string { return &c.LLM.BaseURL })
	b.stringVar("llm-model", l.Model, "model name to ask for explanations",
		func(c *Config) *string { return &c.LLM.Model })
	b.intVar("llm-workers", l.Workers, "LLM worker pool size",
		func(c *Config) *int { return &c.LLM.Workers })
	b.durationVar("llm-timeout", l.Timeout, "timeout for one LLM request attempt",
		func(c *Config) *time.Duration { return &c.LLM.Timeout })
	b.intVar("llm-max-tokens", l.MaxTokens, "cap on the tokens an explanation may use",
		func(c *Config) *int { return &c.LLM.MaxTokens })
	b.floatVar("llm-temperature", l.Temperature, "LLM sampling temperature (low: consistent, parseable output)",
		func(c *Config) *float64 { return &c.LLM.Temperature })
	b.boolVar("llm-json-mode", l.JSONMode, "ask the provider for a JSON object (disable for endpoints that reject response_format)",
		func(c *Config) *bool { return &c.LLM.JSONMode })
	b.intVar("llm-retries", l.Retries, "extra attempts on a transient LLM failure (timeout, 429, 5xx)",
		func(c *Config) *int { return &c.LLM.Retries })

	return b.apply
}

// binder is bind's scratch state: the flag set being filled, and the appliers built
// alongside it.
type binder struct {
	fs    *flag.FlagSet
	apply map[string]applyFunc
}

func (b binder) boolVar(name string, def bool, usage string, field func(*Config) *bool) {
	p := b.fs.Bool(name, def, usage)
	b.apply[name] = func(c *Config) { *field(c) = *p }
}

func (b binder) intVar(name string, def int, usage string, field func(*Config) *int) {
	p := b.fs.Int(name, def, usage)
	b.apply[name] = func(c *Config) { *field(c) = *p }
}

func (b binder) floatVar(name string, def float64, usage string, field func(*Config) *float64) {
	p := b.fs.Float64(name, def, usage)
	b.apply[name] = func(c *Config) { *field(c) = *p }
}

func (b binder) stringVar(name, def, usage string, field func(*Config) *string) {
	p := b.fs.String(name, def, usage)
	b.apply[name] = func(c *Config) { *field(c) = *p }
}

func (b binder) durationVar(name string, def time.Duration, usage string, field func(*Config) *time.Duration) {
	p := b.fs.Duration(name, def, usage)
	b.apply[name] = func(c *Config) { *field(c) = *p }
}

// listVar binds a repeatable flag. It has no default: repeating a flag appends, so a
// default value could only ever be prepended to what the user typed.
func (b binder) listVar(name, usage string, field func(*Config) *[]string) {
	var list stringList
	b.fs.Var(&list, name, usage)
	b.apply[name] = func(c *Config) { *field(c) = list }
}

// stringList is a repeatable string flag (e.g. --docker-label k=v --docker-label x=y).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// splitArgs partitions args at the first "--" separator: everything before is
// returned as flag args, everything after as the subprocess argv (nil when there is
// no separator).
func splitArgs(args []string) (flagArgs, argv []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}
