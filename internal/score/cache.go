// SPDX-License-Identifier: Apache-2.0

package score

import "time"

// minSweep is the fewest entries worth sweeping for.
const minSweep = 64

// entry is one explained template: when it was explained, and how many times it has
// recurred since. The count is what a card shows as "x412" instead of escalating 412
// times.
type entry struct {
	at    time.Time
	count int
}

// Cache remembers which templates have already been escalated for explanation, keyed
// by template hash. It is what makes a recurring error quiet: the same error is
// explained once, and every later occurrence only increments a counter. Entries
// expire after ttl, so an error that returns an hour later is worth explaining again.
//
// Not safe for concurrent use: it is owned by the pipeline goroutine, like the rest
// of the scorer.
//
// An expired entry is a miss whether it is there or not, so keeping one only costs memory, and
// the entries would otherwise last the whole run. They go in a sweep, when the map has doubled
// since the last one, so a Mark stays O(1) on average at any --rate-limit. That holds the map
// under twice its live entries, of which there are at most rate_limit x ttl + rate_limit: the
// limiter is the only way in. (#126)
type Cache struct {
	ttl     time.Duration
	entries map[string]*entry
	sweepAt int // the size that triggers the next sweep
}

// NewCache returns an empty explanation cache with the given TTL.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, entries: make(map[string]*entry), sweepAt: minSweep}
}

// Hit reports whether hash already has a fresh explanation, bumping that entry's
// recurrence count when it does. A hit means "do not escalate again"; an expired
// entry is a miss, so the template can be explained afresh.
func (c *Cache) Hit(hash string, now time.Time) bool {
	e, ok := c.entries[hash]
	if !ok || now.Sub(e.at) >= c.ttl {
		return false
	}
	e.count++
	return true
}

// Mark records that hash has been escalated for explanation, starting its TTL. It
// replaces any expired entry, which resets the recurrence count for the new episode.
func (c *Cache) Mark(hash string, now time.Time) {
	c.entries[hash] = &entry{at: now, count: 1}
	if len(c.entries) >= c.sweepAt {
		c.sweep(now)
	}
}

// sweep deletes the expired entries and sets the size of the next sweep.
func (c *Cache) sweep(now time.Time) {
	for hash, e := range c.entries {
		if now.Sub(e.at) >= c.ttl {
			delete(c.entries, hash)
		}
	}
	c.sweepAt = max(2*len(c.entries), minSweep)
}

// Count returns how many occurrences of hash the current cache entry covers, or 0 if
// it has none. M5 renders this on the card; M3 uses it to prove the cache is what is
// keeping the tool quiet.
func (c *Cache) Count(hash string, now time.Time) int {
	e, ok := c.entries[hash]
	if !ok || now.Sub(e.at) >= c.ttl {
		return 0
	}
	return e.count
}
