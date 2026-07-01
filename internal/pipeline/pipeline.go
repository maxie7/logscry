// SPDX-License-Identifier: Apache-2.0

// Package pipeline normalizes, templates, and deduplicates log lines. In M0 it
// is a pass-through so the end-to-end path compiles and runs.
package pipeline

import "github.com/maxie7/logscry/internal/model"

// Process runs a single line through the pipeline and returns the enriched line.
//
// TODO(M2): normalize -> template -> dedup/count. For M0 this is a pass-through.
func Process(line model.LogLine) model.LogLine {
	return line
}
