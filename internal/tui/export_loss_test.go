// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// lossModel is a sized model whose export has lost *n records so far.
func lossModel(t *testing.T, n *uint64, width int) Model {
	t.Helper()
	m := New(nil, nil, Options{ExportLost: func() uint64 { return *n }})
	sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	return sized.(Model)
}

// snap applies one pipeline snapshot, which is when the TUI looks at the export's count.
func snap(m Model) Model {
	applied, _ := m.Update(snapshotMsg(testSnapshot()))
	return applied.(Model)
}

// TestExportLossNotice: the first time export records are lost, the TUI says so in the
// background-notice line, once. Before #70 a TUI user learnt of it only after quitting.
func TestExportLossNotice(t *testing.T) {
	var n uint64
	m := snap(lossModel(t, &n, 120))
	if len(m.errs) != 0 {
		t.Fatalf("a notice before anything was lost: %v", m.errs)
	}
	n = 3
	m = snap(m)
	n = 5
	m = snap(m)
	notices := 0
	for _, e := range m.errs {
		if strings.Contains(e, "export: records are being lost") {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("%d loss notices, want exactly 1: %v", notices, m.errs)
	}
	if !strings.Contains(m.View(), "records are being lost") {
		t.Error("the notice is not on screen")
	}
}

// TestExportLostStatusField: the status bar carries the running count of lost records, only
// once there is one, in the protected segment that a long source list gives way to.
func TestExportLostStatusField(t *testing.T) {
	var n uint64
	m := snap(lossModel(t, &n, 120))
	if strings.Contains(m.viewStatus(), "export lost") {
		t.Errorf("the field shows with nothing lost:\n%s", m.viewStatus())
	}
	n = 3
	m = snap(m)
	if !strings.Contains(m.viewStatus(), "export lost 3") {
		t.Errorf("the field is missing at 3 lost:\n%s", m.viewStatus())
	}

	narrow := lossModel(t, &n, 80)
	s := testSnapshot()
	s.Stats.Sources = []string{"docker:api", "docker:db", "docker:worker", "docker:cache", "journald:nginx"}
	applied, _ := narrow.Update(snapshotMsg(s))
	if status := applied.(Model).viewStatus(); !strings.Contains(status, "export lost 3") {
		t.Errorf("the field gave way to the source list at width 80:\n%s", status)
	}
}

// TestNoExportNoLossField: without --export there is nothing to count and nothing shows.
func TestNoExportNoLossField(t *testing.T) {
	m := snap(newTestModel(t))
	if strings.Contains(m.viewStatus(), "export lost") || len(m.errs) != 0 {
		t.Errorf("an export field or notice with no export:\n%s\n%v", m.viewStatus(), m.errs)
	}
}
