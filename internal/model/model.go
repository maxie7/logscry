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

// RecentCap is the capacity of Template.Recent. It lives here because the ring is the
// evidence burst detection runs on: the pipeline fills it and the scorer reads it.
//
// The size is chosen against the burst window (10s by default): at 2048 the ring spans
// the whole window for any template under ~200 lines/sec, so the measured rate is the
// honest 10-second average and cannot be skewed by a sub-second clump of lines — a
// batched flush must not read as a spike. Busier templates than that saturate the
// ring, and the scorer measures their rate over the span it actually holds instead
// (see score.windowRate).
//
// It costs 48KB for a template that has actually seen 2048 lines; the slice grows
// lazily, so quiet templates cost a few hundred bytes.
const RecentCap = 2048

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
