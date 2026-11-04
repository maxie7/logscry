// SPDX-License-Identifier: Apache-2.0

package score

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

func TestCacheHitsUntilTTLExpires(t *testing.T) {
	c := NewCache(time.Hour)

	if c.Hit("abc", t0) {
		t.Error("an unexplained template reported as cached")
	}

	c.Mark("abc", t0)
	if !c.Hit("abc", t0.Add(59*time.Minute)) {
		t.Error("an explained template was not cached inside its TTL")
	}
	if c.Hit("abc", t0.Add(time.Hour)) {
		t.Error("the entry survived its TTL")
	}
}

// TestCacheCountsRecurrences: the point of the cache is that a recurring error is
// explained once and then merely counted. That count is what a card shows instead of
// escalating four hundred times.
func TestCacheCountsRecurrences(t *testing.T) {
	c := NewCache(time.Hour)
	c.Mark("abc", t0)

	for i := range 400 {
		if !c.Hit("abc", t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("occurrence %d was not absorbed by the cache", i)
		}
	}

	if got, want := c.Count("abc", t0), 401; got != want {
		t.Errorf("Count = %d, want %d (the initial explanation plus 400 recurrences)", got, want)
	}
}

// TestCacheReExplainsAfterTTL: an error that comes back an hour later is worth
// explaining again — the situation has had time to change.
func TestCacheReExplainsAfterTTL(t *testing.T) {
	cfg := liveConfig()
	cfg.CacheTTL = time.Hour
	f := newFixture(t, cfg, nil)

	if res := f.feed(fault("upstream refused the connection"), t0); !res.Escalate {
		t.Fatal("first occurrence did not escalate")
	}
	// Well past the novelty cooloff, but inside the cache TTL: still quiet.
	if res := f.feed(fault("upstream refused the connection"), t0.Add(30*time.Minute)); res.Escalate {
		t.Fatal("re-escalated inside the cache TTL")
	}
	// Past the TTL: novel again (long unseen) and no longer explained.
	res := f.feed(fault("upstream refused the connection"), t0.Add(2*time.Hour))
	if !res.Escalate {
		t.Errorf("did not re-escalate after the cache TTL expired: score %.2f, reasons %v",
			res.Score, res.Reasons)
	}

	if got := f.sc.Stats().Escalated; got != 2 {
		t.Errorf("Escalated = %d, want 2 (once per episode)", got)
	}
}

// TestCacheForgetsExpiredEntries: an entry past its TTL is a miss whether it is there or not, so
// keeping it is only a leak: one entry per flag, for the whole run (#126).
func TestCacheForgetsExpiredEntries(t *testing.T) {
	const ttl, live = 100 * time.Second, 100
	c := NewCache(ttl)
	for i := range 100_000 {
		c.Mark(fmt.Sprintf("h%d", i), t0.Add(time.Duration(i)*time.Second))
		if n := len(c.entries); n > 2*live+minSweep {
			t.Fatalf("after %d marks the cache holds %d entries, %d of them live", i, n, live)
		}
	}
}

// TestCacheSweepChangesNoAnswer runs a cache against a model that never forgets: forgetting must
// be invisible to Hit and Count, including the recurrence count of an entry that survives a sweep.
func TestCacheSweepChangesNoAnswer(t *testing.T) {
	type stamped struct {
		at time.Time
		n  int
	}
	const ttl = 90 * time.Second
	c := NewCache(ttl)
	never := make(map[string]*stamped)
	r := rand.New(rand.NewPCG(1, 2))
	now := t0
	for i := range 200_000 {
		now = now.Add(time.Duration(r.IntN(500)) * time.Millisecond)
		h := fmt.Sprintf("h%d", r.IntN(4000))
		e, ok := never[h]
		fresh := ok && now.Sub(e.at) < ttl
		if got := c.Hit(h, now); got != fresh {
			t.Fatalf("step %d: Hit(%s) = %v, the never-forgetting model says %v", i, h, got, fresh)
		}
		if fresh {
			e.n++
			if got := c.Count(h, now); got != e.n {
				t.Fatalf("step %d: Count(%s) = %d, want %d", i, h, got, e.n)
			}
			continue
		}
		c.Mark(h, now)
		never[h] = &stamped{at: now, n: 1}
	}
}
