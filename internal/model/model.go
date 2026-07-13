// SPDX-License-Identifier: Apache-2.0

// Package model holds the shared types that flow through the logscry pipeline.
package model

import "time"

// Stream identifies which standard stream a log line came from.
type Stream int

const (
	// Stdout is the standard output stream.
	Stdout Stream = iota
	// Stderr is the standard error stream.
	Stderr
)

// LogLine is a single ingested log line. Fields below "filled by the pipeline"
// are populated during normalization (see internal/pipeline).
type LogLine struct {
	Time   time.Time // parsed if available, else receipt time
	Source string    // e.g. "docker:api", "stdin", "proc:./myapp"
	Stream Stream
	Raw    string

	// filled by the pipeline:
	Level   string // "ERROR"/"WARN"/... if detected, else ""
	Message string // message body after stripping structured prefixes
}

// RecentCap is the capacity of Template.Recent. It lives here because the ring is
// the evidence burst detection runs on: the pipeline fills it and the scorer reads
// it, and the scorer rejects a burst floor it could never reach (see score.Config).
// It must stay comfortably above that floor, or a "50 in 10s" burst would be
// invisible simply because the ring forgot the first half of it.
const RecentCap = 256

// Template is the masked signature of a class of log lines, plus the running
// state used for dedup, burst detection, and explanation caching.
type Template struct {
	Hash        string // signature of the masked line
	Pattern     string // human-readable masked form, e.g. "user <NUM> failed"
	FirstSeen   time.Time
	LastSeen    time.Time
	Count       int
	Recent      []time.Time // ring buffer of recent occurrences for burst detection
	Explained   bool
	Explanation string // last LLM explanation, if any
}
