// SPDX-License-Identifier: Apache-2.0

package main

import "runtime/debug"

// version is the ldflags override, set at release build time with
// -X main.version=$(git describe --tags --always --dirty). It is empty for a plain
// `go build`/`go install`, where BuildInfo supplies the module version instead.
var version string

// versionString resolves the version to display, reading BuildInfo (nil when it is
// unavailable) and applying the resolution order in resolveVersion. It never errors:
// the worst case is the honest fallback "dev".
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return resolveVersion(version, bi)
}

// resolveVersion picks the version: an ldflags override first, then the module version
// from BuildInfo (ignoring the "(devel)" placeholder that a plain `go build`/`go run`
// outside a tagged commit stamps), then "dev". bi may be nil when BuildInfo is
// unavailable — fall back silently, never error.
//
// The hybrid matters: ldflags alone leaves `go install .../logscry@latest` — which never
// runs the Makefile — reporting "dev", so BuildInfo is what makes that install path
// report the real tag.
func resolveVersion(injected string, bi *debug.BuildInfo) string {
	if injected != "" {
		return injected
	}
	if bi != nil && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v := bi.Main.Version
		if buildSetting(bi, "vcs.modified") == "true" {
			v += "-dirty" // honest about a build from an uncommitted tree
		}
		return v
	}
	return "dev"
}

// buildSetting returns the value of a -buildinfo setting (e.g. "vcs.modified"), or "" if
// it is absent.
func buildSetting(bi *debug.BuildInfo, key string) string {
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}
