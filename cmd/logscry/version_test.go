// SPDX-License-Identifier: Apache-2.0

package main

import (
	"runtime/debug"
	"testing"
)

// TestResolveVersion covers the resolution order: an ldflags override wins, otherwise the
// BuildInfo module version (with "(devel)" treated as absent), otherwise "dev".
func TestResolveVersion(t *testing.T) {
	buildInfo := func(v string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}, Settings: settings}
	}

	tests := []struct {
		name     string
		injected string
		bi       *debug.BuildInfo
		want     string
	}{
		{"ldflags override wins", "v9.9.9", buildInfo("v1.2.3"), "v9.9.9"},
		{"ldflags override wins over nil buildinfo", "v9.9.9", nil, "v9.9.9"},
		{"buildinfo module version", "", buildInfo("v1.2.3"), "v1.2.3"},
		{"devel placeholder falls back", "", buildInfo("(devel)"), "dev"},
		{"empty module version falls back", "", buildInfo(""), "dev"},
		{"nil buildinfo falls back", "", nil, "dev"},
		{
			"dirty buildinfo tree is marked",
			"",
			buildInfo("v1.2.3", debug.BuildSetting{Key: "vcs.modified", Value: "true"}),
			"v1.2.3-dirty",
		},
		{
			"clean buildinfo tree is not marked",
			"",
			buildInfo("v1.2.3", debug.BuildSetting{Key: "vcs.modified", Value: "false"}),
			"v1.2.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.injected, tt.bi); got != tt.want {
				t.Errorf("resolveVersion(%q, %v) = %q, want %q", tt.injected, tt.bi, got, tt.want)
			}
		})
	}
}

// TestVersionLine pins what --version prints: the version, then the Go toolchain in brackets.
func TestVersionLine(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		goVersion string
		want      string
	}{
		{"release", "v9.9.9", "go1.2.3", "logscry v9.9.9 (go1.2.3)"},
		{"fallback version", "dev", "go1.2.3", "logscry dev (go1.2.3)"},
		{"experiment suffix kept", "v9.9.9-dirty", "go1.2.3 X:nodwarf5", "logscry v9.9.9-dirty (go1.2.3 X:nodwarf5)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionLine(tt.version, tt.goVersion); got != tt.want {
				t.Errorf("versionLine(%q, %q) = %q, want %q", tt.version, tt.goVersion, got, tt.want)
			}
		})
	}
}
