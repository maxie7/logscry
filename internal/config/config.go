// SPDX-License-Identifier: Apache-2.0

// Package config loads logscry configuration from flags, an optional YAML file,
// and the environment (secrets only, e.g. LOGSCRY_API_KEY).
package config

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Sources / mode.
	Plain bool // --plain: line output instead of the TUI

	// LLM backend.
	LLMBaseURL string
	LLMModel   string
	// APIKey is populated from the LOGSCRY_API_KEY env var, never a flag/file.
	APIKey string

	// TODO: scoring weights + thresholds, rate-limit (calls/min), context window
	// size, cooloff/burst windows, Docker selectors.
}

// Load resolves configuration from flags, an optional config file, and env.
//
// TODO: parse flags, merge --config YAML, read LOGSCRY_API_KEY from env, and
// apply sensible zero-config defaults (works out of the box against local Ollama).
func Load() (Config, error) {
	return Config{}, nil
}
