// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/llm"
	"github.com/maxie7/logscry/internal/pipeline"
	"github.com/maxie7/logscry/internal/score"
)

// TestPinnedSetFitsTheTemplateBound: at the defaults the rate limiter keeps everything that can pin
// a template well under the bound, so a pin can never be what holds the table over it (#126). It
// lives here because this is the one package that can see all three. Retuning any of these numbers
// past that point should be a decision, not an accident.
func TestPinnedSetFitsTheTemplateBound(t *testing.T) {
	const cards = 20 // the pipeline's escalationsKept
	sc, lc := score.Defaults(), llm.Defaults()
	cached := sc.RatePerMin*int(sc.CacheTTL/time.Minute) + sc.RatePerMin
	pinned := cached + cards + lc.Queue + lc.Workers
	if want := 696; pinned != want {
		t.Errorf("%d templates can be pinned at the defaults, want the documented %d", pinned, want)
	}
	if pinned*50 > pipeline.MaxTemplates {
		t.Errorf("%d pinned templates exceed 2%% of the bound", pinned)
	}
	if pinned*(64<<10) > pipeline.MaxTemplateBytes {
		t.Errorf("%d pinned templates at the line cap exceed the byte bound", pinned)
	}
}
