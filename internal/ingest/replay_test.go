// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bufio"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/pipeline"
)

// basicCapture is the committed synthetic capture (see testdata/replay/gen.go).
const basicCapture = "testdata/replay/basic.jsonl"

// replayAll runs a ReplaySource to completion and returns what it emitted.
func replayAll(t *testing.T, src *ReplaySource) ([]model.LogLine, error) {
	t.Helper()
	out := make(chan model.LogLine, 4096)
	err := src.Lines(context.Background(), out)
	close(out)
	var got []model.LogLine
	for l := range out {
		got = append(got, l)
	}
	return got, err
}

func writeCapture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func rawLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// TestReplayUsesJournaldDecode: a replayed entry is exactly what the live journald source
// makes of the same bytes — the level PRIORITY sets (#24), the stderr tag an error-class
// priority gets, the journald:<unit> source, and the entry's own timestamp. A replay that
// bypassed decode would be replaying a different program.
func TestReplayUsesJournaldDecode(t *testing.T) {
	got, err := replayAll(t, NewReplaySource(basicCapture, 0))
	if err != nil {
		t.Fatal(err)
	}
	raws := rawLines(t, basicCapture)
	if len(got) != len(raws) {
		t.Fatalf("replayed %d lines of %d", len(got), len(raws))
	}
	for i, raw := range raws {
		want := decode(model.LogLine{Source: "replay", Stream: model.Stdout, Raw: raw})
		if got[i] != want {
			t.Fatalf("line %d: replay produced %+v, the journald decode %+v", i, got[i], want)
		}
	}
	// And the decode was not a no-op: the fixture's faults carry what they should.
	var sawFatal, sawStderr bool
	for _, l := range got {
		sawFatal = sawFatal || l.Level == "FATAL" && l.Source == "journald:kernel"
		sawStderr = sawStderr || l.Stream == model.Stderr
	}
	if !sawFatal || !sawStderr {
		t.Fatalf("decode did not run: FATAL from the kernel=%v, any stderr=%v", sawFatal, sawStderr)
	}
}

// TestReplayRejectsUntimedCapture: replay needs a clock, and a file with no timestamps
// would replay with a frozen one — every line inside one instant, which is precisely the
// silent failure piping a capture through stdin already produces. It fails before
// emitting anything, and says what it needs.
func TestReplayRejectsUntimedCapture(t *testing.T) {
	path := writeCapture(t, "ERROR: plain text has no clock", "and neither does this")
	got, err := replayAll(t, NewReplaySource(path, 0))
	if err == nil || !strings.Contains(err.Error(), "journalctl -o json") {
		t.Fatalf("err = %v, want one naming the capture format replay needs", err)
	}
	if len(got) != 0 {
		t.Fatalf("emitted %d lines before refusing", len(got))
	}
}

// TestReplayRejectsMissingFile: a mistyped path is a startup error, not an empty replay.
func TestReplayRejectsMissingFile(t *testing.T) {
	if _, err := replayAll(t, NewReplaySource(filepath.Join(t.TempDir(), "nope.jsonl"), 0)); err == nil {
		t.Fatal("replaying a file that does not exist succeeded")
	}
}

// TestReplayCountsUntimedAndBackwards: past the first line, an entry without a timestamp
// and one whose timestamp runs backwards are both replayed — losing a line is worse — but
// both are counted, because each one is a place the capture's clock could not be followed
// and the user should hear about it. The untimed line keeps a zero time, so the clock
// downstream holds rather than jumping to the wall clock.
func TestReplayCountsUntimedAndBackwards(t *testing.T) {
	path := writeCapture(t,
		`{"MESSAGE":"one","PRIORITY":"6","_SYSTEMD_UNIT":"a.service","__REALTIME_TIMESTAMP":"1788000002000000"}`,
		`{"MESSAGE":"no time","PRIORITY":"6","_SYSTEMD_UNIT":"a.service"}`,
		`not json at all`,
		`{"MESSAGE":"earlier","PRIORITY":"6","_SYSTEMD_UNIT":"a.service","__REALTIME_TIMESTAMP":"1788000001000000"}`,
		`{"MESSAGE":"later","PRIORITY":"6","_SYSTEMD_UNIT":"a.service","__REALTIME_TIMESTAMP":"1788000003000000"}`,
	)
	src := NewReplaySource(path, 0)
	got, err := replayAll(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("replayed %d lines, want all 5", len(got))
	}
	if !got[1].Time.IsZero() || !got[2].Time.IsZero() {
		t.Errorf("untimed lines carry %s and %s, want zero (not the wall clock)", got[1].Time, got[2].Time)
	}
	if src.Untimed() != 2 || src.Backwards() != 1 {
		t.Errorf("Untimed()=%d Backwards()=%d, want 2 and 1", src.Untimed(), src.Backwards())
	}
}

// TestReplayPacing: speed only decides how fast the lines are handed over, never what time
// they carry. At 20× a one-second capture takes about 50ms; at max it does not wait at all;
// and a cancelled context stops a long wait at once, as a clean stop.
func TestReplayPacing(t *testing.T) {
	path := writeCapture(t,
		`{"MESSAGE":"one","PRIORITY":"6","_SYSTEMD_UNIT":"a.service","__REALTIME_TIMESTAMP":"1788000000000000"}`,
		`{"MESSAGE":"two","PRIORITY":"6","_SYSTEMD_UNIT":"a.service","__REALTIME_TIMESTAMP":"1788000001000000"}`,
	)
	timed := func(speed float64) time.Duration {
		start := time.Now()
		got, err := replayAll(t, NewReplaySource(path, speed))
		if err != nil || len(got) != 2 {
			t.Fatalf("speed %g: %d lines, err %v", speed, len(got), err)
		}
		if !got[1].Time.Equal(time.UnixMicro(1788000001000000)) {
			t.Fatalf("speed %g changed a line's time to %s", speed, got[1].Time)
		}
		return time.Since(start)
	}
	if d := timed(20); d < 45*time.Millisecond {
		t.Errorf("20× took %s, want at least ~50ms", d)
	}
	if d := timed(0); d >= 45*time.Millisecond {
		t.Errorf("max speed took %s: it must not sleep", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan model.LogLine, 4)
	done := make(chan error, 1)
	go func() { done <- NewReplaySource(path, 0.001).Lines(ctx, out) }() // a 1000s wait
	<-out
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a cancelled replay returned %v, want a clean stop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling did not interrupt the pacing wait")
	}
}

// TestReplayHashesEqualLiveDecode: no template hash moves. Every replayed line templates
// to the hash the live journald path gives the same bytes, because it IS the live path —
// decode, Normalize, TemplatizeLine — with only the clock changed.
func TestReplayHashesEqualLiveDecode(t *testing.T) {
	got, err := replayAll(t, NewReplaySource(basicCapture, 0))
	if err != nil {
		t.Fatal(err)
	}
	p := pipeline.New(nil)
	for i, raw := range rawLines(t, basicCapture) {
		_, want := pipeline.TemplatizeLine(pipeline.Normalize(decode(model.LogLine{Raw: raw})))
		if ev := p.Process(got[i], got[i].Time); ev.Hash != want {
			t.Fatalf("line %d: replay hash %s, live decode hash %s", i, ev.Hash, want)
		}
	}
}

// Publishable-fixture rules. Captures hold real infrastructure, so anything committed under
// testdata/replay is held to the documentation ranges and the four fields replay reads. A
// real journald entry carries thirty-odd — _HOSTNAME, _MACHINE_ID, _BOOT_ID, _CMDLINE, _UID —
// and none of them may ride along into the repository.
const (
	maxFixtureBytes = 128 << 10
	maxFixtureLines = 1000
)

var (
	fixtureFields = map[string]bool{
		"MESSAGE": true, "PRIORITY": true, "__REALTIME_TIMESTAMP": true,
		"_SYSTEMD_UNIT": true, "SYSLOG_IDENTIFIER": true, "_COMM": true,
	}
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	ipv6Re = regexp.MustCompile(`[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7}`)
	hostRe = regexp.MustCompile(`\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}\b`)

	docPrefixes = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),    // RFC 5737 TEST-NET-1
		netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
		netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
		netip.MustParsePrefix("2001:db8::/32"),   // RFC 3849
	}
	docHostSuffixes = []string{".example.com", ".example.net", ".example.org", ".example", ".test", ".invalid"}
)

func isDocAddr(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return true // not an address at all: not this rule's business
	}
	for _, p := range docPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// TestReplayFixturesArePublishable enforces the rules on every committed capture.
func TestReplayFixturesArePublishable(t *testing.T) {
	paths, err := filepath.Glob("testdata/replay/*.jsonl")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no replay fixtures found (err %v)", err)
	}
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > maxFixtureBytes {
			t.Errorf("%s is %d bytes, over the %d cap", path, st.Size(), maxFixtureBytes)
		}
		lines := rawLines(t, path)
		if len(lines) > maxFixtureLines {
			t.Errorf("%s has %d lines, over the %d cap", path, len(lines), maxFixtureLines)
		}
		for i, raw := range lines {
			var entry map[string]string
			if err := json.Unmarshal([]byte(raw), &entry); err != nil {
				t.Errorf("%s:%d is not a flat journald JSON entry: %v", path, i+1, err)
				continue
			}
			for k := range entry {
				if !fixtureFields[k] {
					t.Errorf("%s:%d carries field %s, which replay does not read and a fixture must not hold", path, i+1, k)
				}
			}
			msg := entry["MESSAGE"]
			for _, ip := range ipv4Re.FindAllString(msg, -1) {
				if !isDocAddr(ip) {
					t.Errorf("%s:%d: %s is not in an RFC 5737 documentation range", path, i+1, ip)
				}
			}
			for _, ip := range ipv6Re.FindAllString(msg, -1) {
				if strings.Count(ip, ":") >= 2 && !isDocAddr(strings.Trim(ip, ":")) && !isDocAddr(ip) {
					t.Errorf("%s:%d: %s is not in the RFC 3849 documentation range", path, i+1, ip)
				}
			}
			for _, host := range hostRe.FindAllString(msg, -1) {
				if isDocAddr(host) && ipv4Re.MatchString(host) {
					continue // an address, judged above
				}
				ok := false
				for _, suf := range docHostSuffixes {
					ok = ok || strings.HasSuffix("."+host, suf)
				}
				if !ok {
					t.Errorf("%s:%d: host %q is not under a reserved documentation name", path, i+1, host)
				}
			}
		}
	}
}
