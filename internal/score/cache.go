// SPDX-License-Identifier: Apache-2.0

package score

import "time"

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
type Cache struct {
	ttl     time.Duration
	entries map[string]*entry
}

// NewCache returns an empty explanation cache with the given TTL.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, entries: make(map[string]*entry)}
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
