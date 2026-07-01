// SPDX-License-Identifier: Apache-2.0

package pipeline

import "github.com/maxie7/logscry/internal/model"

// Normalize detects the line format (JSON vs plain text) and fills in Level and
// Message on the LogLine.
//
// TODO(M2): parse JSON for level/severity + msg/message; otherwise run light
// heuristics for a leading level token on plain text.
func Normalize(line model.LogLine) model.LogLine {
	return line
}
