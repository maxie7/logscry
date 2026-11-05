// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/pipeline"
)

// --- the source list after eviction (#163) -----------------------------------------------
//
// Past its bound the list is the names held, not the names seen. Everything that reads it has to
// say so, and an ordinary run must not change by a byte.

func sourcesModel(t *testing.T, width int, stats func(*pipeline.Stats)) Model {
	t.Helper()
	sized, _ := New(nil, nil, Options{DockerTail: "100"}).Update(tea.WindowSizeMsg{Width: width, Height: 24})
	snap := testSnapshot()
	stats(&snap.Stats)
	applied, _ := sized.(Model).Update(snapshotMsg(snap))
	return applied.(Model)
}

func manyNames(n int, prefix string) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return names
}

func TestSourcesPhrase(t *testing.T) {
	for _, tc := range []struct {
		n       int
		partial bool
		want    string
	}{
		{0, false, "no sources yet"},
		{1, false, "1 source"},
		{52, false, "52 sources"},
		{4096, true, "4,096+ sources"},
		{4, true, "4+ sources"}, // the byte bound can leave few: still a true lower bound
		{1, true, "1+ sources"},
	} {
		if got := sourcesPhrase(tc.n, tc.partial); got != tc.want {
			t.Errorf("sourcesPhrase(%d, %v) = %q, want %q", tc.n, tc.partial, got, tc.want)
		}
	}
}

func TestEmptyPaneSaysAtLeast(t *testing.T) {
	m := sourcesModel(t, 120, func(s *pipeline.Stats) {
		s.Sources, s.SourcesPartial = manyNames(4096, "journald:unit-"), true
	})
	if got := m.viewCardsEmpty(100, 12); !strings.Contains(got, "across 4,096+ sources") {
		t.Errorf("the empty pane does not say at least:\n%s", got)
	}
}

// The count and the tail lead, so the width cuts names and not them.
func TestPartialStatusKeepsItsCountAndTail(t *testing.T) {
	for _, width := range []int{100, 120, 200} {
		m := sourcesModel(t, width, func(s *pipeline.Stats) {
			s.Sources = manyNames(4096, "docker:churned-")
			s.SourcesPartial, s.DockerSeen = true, true
		})
		status := m.viewStatus()
		for _, want := range []string{"4,096+ sources", "tail:100"} {
			if !strings.Contains(status, want) {
				t.Errorf("width %d: %q lost to the list:\n%s", width, want, status)
			}
		}
	}
}

// Four names at the line cap fill the byte bound. "4+" is small and still true.
func TestByteBoundShowsASmallCount(t *testing.T) {
	m := sourcesModel(t, 120, func(s *pipeline.Stats) {
		s.Sources, s.SourcesPartial = manyNames(4, "journald:"+strings.Repeat("x", 60<<10)), true
	})
	if status := m.viewStatus(); !strings.Contains(status, "4+ sources") {
		t.Errorf("no lower bound in:\n%s", status)
	}
}

func TestOrdinaryStatusIsUnchanged(t *testing.T) {
	m := sourcesModel(t, 120, func(s *pipeline.Stats) { s.DockerSeen = true })
	status := m.viewStatus()
	if !strings.Contains(status, "docker:api, docker:db · tail:100") {
		t.Errorf("the ordinary list changed:\n%s", status)
	}
	if strings.Contains(status, "+ sources") {
		t.Errorf("a whole list claims to be partial:\n%s", status)
	}
}

// The notice follows Stats.DockerSeen and not the names held, in both directions.
func TestDockerTailFollowsDockerSeen(t *testing.T) {
	gone := sourcesModel(t, 120, func(s *pipeline.Stats) {
		s.Sources = []string{"journald:unit-a"}
		s.SourcesPartial, s.DockerSeen = true, true
	})
	if !strings.Contains(gone.viewStatus(), "tail:100") {
		t.Errorf("the notice went with the evicted name:\n%s", gone.viewStatus())
	}
	never := sourcesModel(t, 120, func(s *pipeline.Stats) { s.Sources = []string{"docker:api"} })
	if strings.Contains(never.viewStatus(), "tail:100") {
		t.Errorf("a notice with DockerSeen false:\n%s", never.viewStatus())
	}
}

func TestPartialStatusNeverOverflows(t *testing.T) {
	for _, width := range []int{80, 100, 120, 200} {
		sized, _ := New(nil, nil, Options{Explain: true, DockerTail: "100"}).
			Update(tea.WindowSizeMsg{Width: width, Height: 24})
		snap := overflowSnapshot()
		snap.Stats.SourcesPartial, snap.Stats.DockerSeen = true, true
		applied, _ := sized.(Model).Update(snapshotMsg(snap))
		for i, line := range strings.Split(applied.(Model).View(), "\n") {
			if got := visWidth(line); got > width {
				t.Errorf("width %d: line %d is %d cells wide:\n%q", width, i, got, line)
			}
		}
	}
}
