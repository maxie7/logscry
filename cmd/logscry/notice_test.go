// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/maxie7/logscry/internal/config"
	"github.com/maxie7/logscry/internal/llm"
)

// TestRemoteHost classifies local endpoints as local (no notice) and off-box ones as
// remote (notice), so the "your logs are leaving the machine" warning fires exactly when it
// should.
func TestRemoteHost(t *testing.T) {
	cases := []struct {
		url        string
		wantRemote bool
	}{
		{"http://localhost:11434/v1", false},
		{"http://127.0.0.1:11434/v1", false},
		{"http://[::1]:11434/v1", false},
		{"http://0.0.0.0:11434/v1", false},
		{"http://ollama.localhost/v1", false},
		{"https://api.openai.com/v1", true},
		{"https://api.groq.com/openai/v1", true},
		{"not a url", false}, // unparseable ⇒ stay quiet, don't warn wrongly
	}
	for _, c := range cases {
		if _, remote := remoteHost(c.url); remote != c.wantRemote {
			t.Errorf("remoteHost(%q) remote = %v, want %v", c.url, remote, c.wantRemote)
		}
	}
}

// TestRemoteWarnHost: the warning is suppressed when masking is on or in dry-run (which
// builds no backend at all), and fires only for a remote endpoint with masking off.
func TestRemoteWarnHost(t *testing.T) {
	base := func() config.Config {
		c := config.Config{LLM: llm.Defaults()}
		c.LLM.BaseURL = "https://api.openai.com/v1"
		return c
	}

	if h := remoteWarnHost(base()); h == "" {
		t.Error("remote endpoint with masking off should warn")
	}

	on := base()
	on.LLM.Anonymize = true
	if h := remoteWarnHost(on); h != "" {
		t.Errorf("masking on should suppress the warning, got %q", h)
	}

	dry := base()
	dry.ExplainDryRun = true
	if h := remoteWarnHost(dry); h != "" {
		t.Errorf("dry-run sends nothing, should suppress the warning, got %q", h)
	}

	local := base()
	local.LLM.BaseURL = "http://localhost:11434/v1"
	if h := remoteWarnHost(local); h != "" {
		t.Errorf("local endpoint should not warn, got %q", h)
	}
}
