// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/maxie7/logscry/internal/model"
)

// keyByName builds the KeyMsg Bubble Tea would deliver for a named key.
func keyByName(name string) tea.KeyMsg {
	switch name {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	}
	panic("unknown key " + name)
}

// TestGlobalKeysSurviveEveryPaneKeySequence brute-forces the state space the reported bug
// lives in: after ANY sequence of pane-scoped keys — any focus, any selection, expanded or
// not, scrolled or not — q, t and p must still work.
func TestGlobalKeysSurviveEveryPaneKeySequence(t *testing.T) {
	paneKeys := []string{"tab", "shift+tab", "up", "down", "enter", "space", "pgup", "pgdown", "home", "end"}

	var walk func(prefix []string, depth int)
	walk = func(prefix []string, depth int) {
		// Rebuild from scratch so each sequence is independent.
		m := cardsModel(t, 3)
		for _, k := range prefix {
			next, _ := m.Update(keyByName(k))
			m = next.(Model)
		}
		name := strings.Join(prefix, ",")
		if name == "" {
			name = "<none>"
		}

		// q must quit.
		if _, cmd := m.Update(key('q')); cmd == nil {
			t.Errorf("after [%s]: 'q' produced no command", name)
		} else if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("after [%s]: 'q' did not quit", name)
		}
		// t must toggle the left view.
		toggled, _ := m.Update(key('t'))
		if toggled.(Model).mode == m.mode {
			t.Errorf("after [%s]: 't' did not toggle the view (still %v)", name, m.mode)
		}
		// p must pause.
		paused, _ := m.Update(key('p'))
		if paused.(Model).paused == m.paused {
			t.Errorf("after [%s]: 'p' did not toggle pause", name)
		}

		if depth == 0 {
			return
		}
		for _, k := range paneKeys {
			walk(append(prefix, k), depth-1)
		}
	}
	walk(nil, 3)
}

// TestGlobalKeysSurviveEveryFocusAndCardState is the same guarantee stated directly over
// the states rather than the paths into them.
func TestGlobalKeysSurviveEveryFocusAndCardState(t *testing.T) {
	states := map[string]func() Model{
		"stream focus, no cards": func() Model {
			m := New(nil, nil, Options{Explain: true})
			sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
			applied, _ := sized.(Model).Update(snapshotMsg(testSnapshot()))
			return applied.(Model)
		},
		"cards focus, no cards": func() Model {
			m := New(nil, nil, Options{Explain: true})
			sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
			applied, _ := sized.(Model).Update(snapshotMsg(testSnapshot()))
			tabbed, _ := applied.(Model).Update(keyByName("tab"))
			return tabbed.(Model)
		},
		"cards focus, card selected": func() Model {
			m := cardsModel(t, 3)
			tabbed, _ := m.Update(keyByName("tab"))
			return tabbed.(Model)
		},
		"cards focus, card expanded": func() Model {
			m := cardsModel(t, 3)
			tabbed, _ := m.Update(keyByName("tab"))
			opened, _ := tabbed.(Model).Update(keyByName("enter"))
			return opened.(Model)
		},
		"cards focus, scrolled to an older card": func() Model {
			m := cardsModel(t, 3)
			tabbed, _ := m.Update(keyByName("tab"))
			down, _ := tabbed.(Model).Update(keyByName("down"))
			return down.(Model)
		},
		"stacked layout, cards focus": func() Model {
			m := New(nil, nil, Options{Explain: true})
			sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			applied, _ := sized.(Model).Update(snapshotMsg(escalationSnapshot(
				&model.Explanation{Hash: "aaa", State: model.ExplainDone, Summary: "boom"})))
			tabbed, _ := applied.(Model).Update(keyByName("tab"))
			return tabbed.(Model)
		},
	}

	for name, build := range states {
		t.Run(name, func(t *testing.T) {
			m := build()
			if _, cmd := m.Update(key('q')); cmd == nil {
				t.Error("'q' produced no command")
			} else if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Error("'q' did not quit")
			}
			if toggled, _ := m.Update(key('t')); toggled.(Model).mode == m.mode {
				t.Error("'t' did not toggle the view")
			}
			if paused, _ := m.Update(key('p')); paused.(Model).paused == m.paused {
				t.Error("'p' did not toggle pause")
			}
		})
	}
}

// TestKeyTraceRecordsWhatTheLoopReceived: the trace is the evidence the next dead-key
// report will be judged on, so it has to record the key AND the state it arrived in. A
// diagnostic that quietly writes nothing is worse than none — it retires a theory it never
// actually tested.
func TestKeyTraceRecordsWhatTheLoopReceived(t *testing.T) {
	var buf bytes.Buffer
	keyTrace = &buf
	t.Cleanup(func() { keyTrace = nil })

	m := cardsModel(t, 2)
	for _, k := range []tea.KeyMsg{keyByName("tab"), key('t'), key('q')} {
		next, _ := m.Update(k)
		m = next.(Model)
	}

	got := buf.String()
	for _, want := range []string{`key="tab"`, `key="t"`, `key="q"`, "focus=cards", "view=AGGREGATED"} {
		if !strings.Contains(got, want) {
			t.Errorf("the key trace is missing %q:\n%s", want, got)
		}
	}
}

// TestKeyTraceIsOffByDefault: it must cost a normal run nothing, and above all must never
// write to stdout, which would corrupt the alternate screen.
func TestKeyTraceIsOffByDefault(t *testing.T) {
	t.Setenv("LOGSCRY_KEYLOG", "")
	if w := openKeyTrace(); w != nil {
		t.Error("the key trace is on without LOGSCRY_KEYLOG")
	}
}

// --- Coalesced rune bursts (the "keyboard dies" bug) ----------------------------------

// TestCoalescedRuneBurstIsDispatchedPerRune is the regression test for the real bug.
//
// Bubble Tea merges printable characters that arrive in one read into a SINGLE KeyRunes
// message: press t then p quickly and the event loop receives one key whose String() is
// "tp". Dispatching on that string matched nothing, and BOTH keystrokes were silently
// dropped — which is what "the keyboard is dead" actually was.
func TestCoalescedRuneBurstIsDispatchedPerRune(t *testing.T) {
	// "tp": toggle the view AND pause, from one message.
	m := cardsModel(t, 2)
	burst, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tp")})
	got := burst.(Model)

	if got.mode != aggregatedView {
		t.Error("'t' was dropped from the burst \"tp\": the view did not toggle")
	}
	if !got.paused {
		t.Error("'p' was dropped from the burst \"tp\": the run did not pause")
	}

	// A burst carrying q must still quit — the keystroke that strands a user in the
	// alternate screen when it goes missing.
	quit, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tq")})
	if cmd == nil {
		t.Fatal("the burst \"tq\" produced no command: 'q' was dropped")
	}
	if !containsQuit(cmd()) {
		t.Error("the burst \"tq\" did not quit")
	}
	if quit.(Model).mode != aggregatedView {
		t.Error("'t' was dropped from the burst \"tq\"")
	}
}

// TestCoalescedBurstSurvivesEveryFocus: the burst is dispatched per rune whichever pane
// holds the focus — the bug was reported after tab, and a fix that only works on the
// stream would look exactly like a fix while still being broken where it was found.
func TestCoalescedBurstSurvivesEveryFocus(t *testing.T) {
	for _, tabs := range []int{0, 1, 2} {
		m := cardsModel(t, 2)
		for range tabs {
			next, _ := m.Update(keyByName("tab"))
			m = next.(Model)
		}
		burst, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tp")})
		got := burst.(Model)

		if got.mode != aggregatedView || !got.paused {
			t.Errorf("after %d tab(s): the burst \"tp\" was dropped (mode=%v paused=%v)",
				tabs, got.mode, got.paused)
		}
	}
}

// TestSingleRuneStillWorks: the burst path must not break the ordinary one keystroke at a
// time case, which is how the keyboard behaves the rest of the time.
func TestSingleRuneStillWorks(t *testing.T) {
	m := cardsModel(t, 2)
	toggled, _ := m.Update(key('t'))
	if toggled.(Model).mode != aggregatedView {
		t.Error("a single 't' stopped toggling the view")
	}
}

// containsQuit reports whether a message (or a batch of them) is tea.Quit.
func containsQuit(msg tea.Msg) bool {
	switch m := msg.(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range m {
			if c != nil && containsQuit(c()) {
				return true
			}
		}
	}
	return false
}
