// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/maxie7/logscry/internal/model"
)

func TestParseDockerTS(t *testing.T) {
	receipt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("valid prefix is parsed and stripped", func(t *testing.T) {
		raw := "2024-03-04T05:06:07.123456789Z hello world"
		gotTime, gotMsg := parseDockerTS(receipt, raw)
		want, _ := time.Parse(time.RFC3339Nano, "2024-03-04T05:06:07.123456789Z")
		if !gotTime.Equal(want) {
			t.Errorf("time = %v, want %v", gotTime, want)
		}
		if gotMsg != "hello world" {
			t.Errorf("msg = %q, want %q", gotMsg, "hello world")
		}
	})

	t.Run("malformed prefix falls back to receipt time and untouched line", func(t *testing.T) {
		for _, raw := range []string{"not-a-timestamp here", "noSpaceAtAll", ""} {
			gotTime, gotMsg := parseDockerTS(receipt, raw)
			if !gotTime.Equal(receipt) {
				t.Errorf("raw %q: time = %v, want receipt %v", raw, gotTime, receipt)
			}
			if gotMsg != raw {
				t.Errorf("raw %q: msg = %q, want unchanged", raw, gotMsg)
			}
		}
	})
}

func TestMatcher(t *testing.T) {
	labels := map[string]string{"app": "web", "env": "prod"}

	tests := []struct {
		name string
		sel  DockerSelector
		id   string
		cn   string
		want bool
	}{
		{"all matches anything", DockerSelector{All: true}, "abc", "whatever", true},
		{"name regex hit", DockerSelector{NameRegex: "^web"}, "abc", "web-1", true},
		{"name regex miss", DockerSelector{NameRegex: "^db"}, "abc", "web-1", false},
		{"explicit id hit", DockerSelector{IDs: []string{"abc"}}, "abc", "web-1", true},
		{"explicit id miss", DockerSelector{IDs: []string{"xyz"}}, "abc", "web-1", false},
		{"all labels present", DockerSelector{Label: []string{"app=web", "env=prod"}}, "abc", "web-1", true},
		{"one label missing", DockerSelector{Label: []string{"app=web", "env=staging"}}, "abc", "web-1", false},
		{"nothing selected", DockerSelector{}, "abc", "web-1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := newMatcher(tc.sel)
			if err != nil {
				t.Fatalf("newMatcher: %v", err)
			}
			if got := m.matches(tc.id, tc.cn, labels); got != tc.want {
				t.Errorf("matches(%q, %q, %v) = %v, want %v", tc.id, tc.cn, labels, got, tc.want)
			}
		})
	}
}

func TestNewMatcherErrors(t *testing.T) {
	if _, err := newMatcher(DockerSelector{NameRegex: "("}); err == nil {
		t.Error("expected error for bad name regexp")
	}
	if _, err := newMatcher(DockerSelector{Label: []string{"nokv"}}); err == nil {
		t.Error("expected error for label without '='")
	}
}

// TestStreamLogsDemuxesNonTTY builds a synthetic multiplexed stream (the 8-byte
// framed format Docker uses when a container has no TTY) and verifies streamLogs
// demuxes it: stdout and stderr land on the right Stream, the timestamp prefix is
// parsed off, and no header bytes leak into the message.
func TestStreamLogsDemuxesNonTTY(t *testing.T) {
	var buf bytes.Buffer
	sw := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	if _, err := sw.Write([]byte("2024-03-04T05:06:07.000000000Z hello out\n")); err != nil {
		t.Fatal(err)
	}
	ew := stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
	if _, err := ew.Write([]byte("2024-03-04T05:06:08.000000000Z hello err\n")); err != nil {
		t.Fatal(err)
	}

	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return streamLogs(ctx, &buf, false, "docker:x", out)
	})

	byStream := map[model.Stream]model.LogLine{}
	for _, ll := range got {
		byStream[ll.Stream] = ll
		if ll.Source != "docker:x" {
			t.Errorf("unexpected source %q", ll.Source)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(got), got)
	}
	if out := byStream[model.Stdout]; out.Raw != "hello out" {
		t.Errorf("stdout line = %q, want %q (header/timestamp leak?)", out.Raw, "hello out")
	}
	if er := byStream[model.Stderr]; er.Raw != "hello err" {
		t.Errorf("stderr line = %q, want %q (header/timestamp leak?)", er.Raw, "hello err")
	}
	want, _ := time.Parse(time.RFC3339Nano, "2024-03-04T05:06:07.000000000Z")
	if out := byStream[model.Stdout]; !out.Time.Equal(want) {
		t.Errorf("stdout time = %v, want %v", out.Time, want)
	}
}

// TestStreamLogsTTYRaw verifies the TTY path: a raw, unframed stream is read
// directly and every line is tagged Stdout.
func TestStreamLogsTTYRaw(t *testing.T) {
	input := "2024-03-04T05:06:07.000000000Z line one\n2024-03-04T05:06:08.000000000Z line two\n"
	got := collect(t, func(ctx context.Context, out chan<- model.LogLine) error {
		return streamLogs(ctx, strings.NewReader(input), true, "docker:x", out)
	})

	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(got), got)
	}
	want := []string{"line one", "line two"}
	for i, ll := range got {
		if ll.Stream != model.Stdout {
			t.Errorf("line %d stream = %v, want Stdout", i, ll.Stream)
		}
		if ll.Raw != want[i] {
			t.Errorf("line %d = %q, want %q", i, ll.Raw, want[i])
		}
	}
}
