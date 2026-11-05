// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"container/list"
	"slices"
	"strings"
)

// dockerPrefix starts every Docker source's name. ingest builds the name in docker.go
// ("docker:"+name, in followContainer); this is the one place that reads it back (see
// Stats.DockerSeen).
const dockerPrefix = "docker:"

// sourceSet is the status bar's list of source names: the most recently seen, within MaxSources
// and MaxSourceBytes. Without the bound a stream of distinct names grew it for as long as the run
// lasted, and the snapshot sorted all of it on the pipeline goroutine, because a journald entry
// names its own source (#163).
//
// Nothing but the list depends on a name being held. A line, a card, the context ring and the
// export each carry their own copy of the name, so a name can go without a pin, unlike a template
// (see Pipeline.pinned), and going costs no line and no score.
//
// An exact count of names ever seen would take a memory of every name, which is the thing being
// bounded, so nothing here claims one. partial says only that some were dropped. Like collector,
// a sourceSet is confined to the pipeline goroutine. The limits are fields so that a test can set
// small ones.
type sourceSet struct {
	names     map[string]*list.Element // name -> its element in order, whose Value is the name
	order     *list.List               // every held name, least recently seen at the front
	bytes     int                      // the sum of len(name) over the held names
	maxNames  int
	maxBytes  int
	evictions *Evictions // counts them; nil counts nothing

	// Both are sticky, because each describes the run and not the names held now.
	partial bool // a name has been dropped, so more were seen than are held
	docker  bool // a Docker name has been seen, whether or not it is still held
}

func newSourceSet() *sourceSet {
	return &sourceSet{
		names:    make(map[string]*list.Element),
		order:    list.New(),
		maxNames: MaxSources,
		maxBytes: MaxSourceBytes,
	}
}

// see records that name was on the line just processed. It runs once a line, so it is O(1): a
// known name is a lookup and a move, and each eviction was paid for by that name's insertion.
//
// The name just seen is never its own victim, whatever its length: Len > 1 stops the loop before
// it reaches the back. The byte bound is above the line cap, so this matters only to a test.
func (s *sourceSet) see(name string) {
	if e, ok := s.names[name]; ok {
		s.order.MoveToBack(e)
		return
	}
	s.names[name] = s.order.PushBack(name)
	s.bytes += len(name)
	if strings.HasPrefix(name, dockerPrefix) {
		s.docker = true
	}
	for s.over() && s.order.Len() > 1 {
		s.evict(s.order.Front())
	}
}

// over reports whether the list is past either bound.
func (s *sourceSet) over() bool {
	return s.order.Len() > s.maxNames || s.bytes > s.maxBytes
}

// evict drops the name at e.
func (s *sourceSet) evict(e *list.Element) {
	name := e.Value.(string)
	s.order.Remove(e)
	delete(s.names, name)
	s.bytes -= len(name)
	s.partial = true
	s.evictions.add()
}

// sorted copies the held names out in order. A fresh copy each snapshot keeps Snapshot's promise
// that it shares nothing with this goroutine.
//
// It is not cached. A cache pays only for a set both large and unchanging: a stream churning the
// list changes it every tick, which is the worst case and stays as it is, and on a real host (52
// names at most across ten boots) there is nothing to save. The cost is bounded by MaxSources, and
// an idle stream builds no snapshot at all.
func (s *sourceSet) sorted() []string {
	out := make([]string, 0, len(s.names))
	for name := range s.names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
