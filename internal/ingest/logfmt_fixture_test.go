// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"bufio"
	"os"
	"testing"

	"github.com/maxie7/logscry/internal/ingest"
	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// TestFixtureLinesKeepTheirTemplate is #83's hash-stability guard on the committed replay
// fixture: the logfmt branch moves template hashes for logfmt lines only, and the fixture
// holds none, so every one of its lines must reach the text path through the dispatcher
// byte for byte — the same composition it had before the branch existed.
func TestFixtureLinesKeepTheirTemplate(t *testing.T) {
	f, err := os.Open("testdata/replay/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := pipeline.Normalize(ingest.Decode(model.LogLine{Source: "journald", Raw: sc.Text()}))
		gotPattern, gotHash := pipeline.TemplatizeLine(line)
		wantPattern, wantHash := pipeline.Templatize(line.Message)
		if gotPattern != wantPattern || gotHash != wantHash {
			t.Errorf("line %d left the text path: %q/%s, want %q/%s",
				n+1, gotPattern, gotHash, wantPattern, wantHash)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("the fixture is empty; the guard would pass vacuously")
	}
}
