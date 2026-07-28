// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// jsonLine builds a line carrying raw JSON, normalized the way Process does.
func jsonLine(raw string) model.LogLine {
	return Normalize(model.LogLine{Raw: raw})
}

// pattern is the signature TemplatizeLine gives raw.
func pattern(raw string) string {
	p, _ := TemplatizeLine(jsonLine(raw))
	return p
}

// hash is the template hash TemplatizeLine gives raw.
func hash(raw string) string {
	_, h := TemplatizeLine(jsonLine(raw))
	return h
}

// TestJSONShapeReadable is the dogfooding case that motivated M9: an MCP-style nested
// object with no level/msg. The text masker turned every string — KEYS INCLUDED — into
// <STR> and produced brace soup; the signature must instead read like the event, with
// field names intact and only values masked.
func TestJSONShapeReadable(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":7,"method":"tools/call",` +
		`"params":{"name":"search","arguments":{"query":"logs","limit":20}}}`
	got := pattern(raw)

	want := `{"id":<NUM>,"jsonrpc":<STR>,"method":<STR>,` +
		`"params":{"arguments":{"limit":<NUM>,"query":<STR>},"name":<STR>}}`
	if got != want {
		t.Errorf("signature = %q,\n            want %q", got, want)
	}
	for _, key := range []string{"jsonrpc", "method", "params", "arguments", "query"} {
		if !strings.Contains(got, strconv.Quote(key)) {
			t.Errorf("key %q was masked away: %q", key, got)
		}
	}
	if strings.Contains(got, `"search"`) || strings.Contains(got, `"logs"`) || strings.Contains(got, "20") {
		t.Errorf("a value survived into the signature: %q", got)
	}
}

// TestJSONShapeTypedLeaves pins the placeholder vocabulary: it is the text masker's, so a
// reader does not have to learn a second one.
func TestJSONShapeTypedLeaves(t *testing.T) {
	got := pattern(`{"s":"x","n":1,"f":-2.5,"b":true,"c":false,"z":null}`)
	want := `{"b":<BOOL>,"c":<BOOL>,"f":<NUM>,"n":<NUM>,"s":<STR>,"z":<NULL>}`
	if got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// TestJSONDedupProperties is the property the epic exists for: same keys collapse
// whatever the values, different keys do not collapse, and key ORDER is irrelevant.
func TestJSONDedupProperties(t *testing.T) {
	sameShape := hash(`{"svc":"api","code":500,"ok":false}`)
	otherValues := hash(`{"svc":"worker","code":404,"ok":true}`)
	if sameShape != otherValues {
		t.Errorf("same keys, different values split templates: %s vs %s", sameShape, otherValues)
	}

	if differentKeys := hash(`{"svc":"api","status":500,"ok":false}`); differentKeys == sameShape {
		t.Errorf("different keys collided on %s", sameShape)
	}

	if reordered := hash(`{"ok":false,"code":500,"svc":"api"}`); reordered != sameShape {
		t.Errorf("key order split templates: %s vs %s", reordered, sameShape)
	}

	// Nesting is structure, not order: the same field one level deeper is a different event.
	if nested := hash(`{"svc":"api","code":500,"ok":{"ok":false}}`); nested == sameShape {
		t.Errorf("nesting difference collided on %s", sameShape)
	}
}

// TestJSONMessageFragmentMatchesTextPath is the narrow boundary the design turns on. The
// recognized message field — and ONLY it — is run through the text masker, so the fragment
// inside a JSON signature is byte-identical to the template the same text gets on the
// plain-text path. The full hashes still differ: a structured event and an unstructured one
// are genuinely different events, and the JSON wrapper carries real information (sibling
// fields, and where severity comes from).
func TestJSONMessageFragmentMatchesTextPath(t *testing.T) {
	textPattern, textHash := Templatize("user 4821 failed")
	jsonPattern, jsonHash := TemplatizeLine(jsonLine(`{"level":"error","msg":"user 4821 failed","ts":1723}`))

	if !strings.Contains(jsonPattern, strconv.Quote(textPattern)) {
		t.Errorf("message fragment in %q is not the text template %q", jsonPattern, textPattern)
	}
	if want := `{"level":<STR>,"msg":"user <NUM> failed","ts":<NUM>}`; jsonPattern != want {
		t.Errorf("signature = %q, want %q", jsonPattern, want)
	}
	if jsonHash == textHash {
		t.Error("a JSON event and a plain-text event merged into one template")
	}

	// The point of masking the message rather than blanking it: genuinely different
	// events at the same JSON shape stay different templates.
	full := hash(`{"level":"error","msg":"disk full","ts":9}`)
	refused := hash(`{"level":"error","msg":"connection refused","ts":9}`)
	if full == refused {
		t.Error("different messages at the same shape collapsed into one template")
	}
	// ...while the same event with different variable parts still collapses.
	if a, b := hash(`{"level":"error","msg":"user 1 failed","ts":9}`),
		hash(`{"level":"error","msg":"user 2 failed","ts":9}`); a != b {
		t.Errorf("variable-only difference split templates: %s vs %s", a, b)
	}
}

// TestJSONMessageMaskingIsMessageOnly guards the boundary from the other side: every
// value that is not the message field stays a plain typed placeholder. Running the text
// masker over arbitrary values would drag the brace soup straight back in.
func TestJSONMessageMaskingIsMessageOnly(t *testing.T) {
	got := pattern(`{"msg":"from 10.0.0.1","peer":"10.0.0.2","note":"user 5 failed"}`)
	want := `{"msg":"from <IP>","note":<STR>,"peer":<STR>}`
	if got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// TestJSONMessageFieldEdges: the text-masking exception is worth making only when the
// message is unmistakable. Two candidate fields, or a non-string value, fall back to an
// ordinary typed placeholder rather than guessing.
func TestJSONMessageFieldEdges(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			"ambiguous candidates",
			`{"msg":"user 4821 failed","message":"user 4821 failed"}`,
			`{"message":<STR>,"msg":<STR>}`,
		},
		{
			"object-valued msg",
			`{"msg":{"code":1},"svc":"api"}`,
			`{"msg":{"code":<NUM>},"svc":<STR>}`,
		},
		{
			"number-valued msg",
			`{"msg":42}`,
			`{"msg":<NUM>}`,
		},
		{
			"null-valued msg",
			`{"msg":null}`,
			`{"msg":<NULL>}`,
		},
		{
			// Case is a logger's choice, not a different field.
			"uppercase msg key",
			`{"MSG":"user 4821 failed"}`,
			`{"MSG":"user <NUM> failed"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pattern(tt.raw); got != tt.want {
				t.Errorf("signature = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestJSONMessageContainingJSON pins the decision for v0.7.0 on a message value that is
// itself JSON-as-a-string — structured loggers do this constantly. It is NOT re-parsed:
// it goes through the text masker like any other string. Deterministic, and recursing into
// message values is a different feature.
func TestJSONMessageContainingJSON(t *testing.T) {
	raw := `{"level":"info","msg":"{\"event\":\"start\",\"id\":7}"}`
	inner, _ := Templatize(`{"event":"start","id":7}`)

	got := pattern(raw)
	if !strings.Contains(got, strconv.Quote(inner)) {
		t.Errorf("signature = %q, want the message text-masked as %q", got, inner)
	}
	if want := `{"level":<STR>,"msg":"{<STR>:<STR>,<STR>:<NUM>}"}`; got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// TestJSONEmptyObjects: an empty object is a valid and common heartbeat/ack line. It
// renders as "{}", which means every field-less line from every source lands on ONE
// template. That is accepted deliberately — they ARE structurally identical, and a line
// carrying no fields carries nothing to tell it apart by; a flood of them reads as a burst
// on one template, which is the honest picture.
func TestJSONEmptyObjects(t *testing.T) {
	if got := pattern(`{}`); got != "{}" {
		t.Errorf("empty object = %q, want {}", got)
	}
	if hash(`{}`) != hash(`  {}  `) {
		t.Error("an empty object and a padded empty object split templates")
	}
	if got := pattern(`{"data":{},"tags":[],"meta":{"inner":{}}}`); got != `{"data":{},"meta":{"inner":{}},"tags":[]}` {
		t.Errorf("empty nested containers = %q", got)
	}
	// Two heartbeats from different sources are the same template, by design.
	a, _ := TemplatizeLine(model.LogLine{Source: "docker:api", Raw: "{}"})
	b, _ := TemplatizeLine(model.LogLine{Source: "docker:worker", Raw: "{}"})
	if a != b {
		t.Errorf("empty-object signature varies by source: %q vs %q", a, b)
	}
}

// TestJSONArrays: a top-level array takes the shape branch too — keys inside its elements
// would otherwise be masked away, exactly the failure this epic fixes. Element COUNT is a
// value-like property, not structure, so it must not split a template.
func TestJSONArrays(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"top-level array of objects", `[{"id":1,"ok":true},{"id":2,"ok":false}]`, `[{"id":<NUM>,"ok":<BOOL>}]`},
		{"scalars collapse by type", `[1,2,3]`, `[<NUM>]`},
		{"mixed types kept distinct", `[1,"a",true]`, `[<BOOL>,<NUM>,<STR>]`},
		{"empty array", `[]`, `[]`},
		{"nested array", `{"tags":["a","b","c"]}`, `{"tags":[<STR>]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pattern(tt.raw); got != tt.want {
				t.Errorf("signature = %q, want %q", got, tt.want)
			}
		})
	}

	if short, long := hash(`{"tags":["a"]}`), hash(`{"tags":["a","b","c","d"]}`); short != long {
		t.Errorf("array length split templates: %s vs %s", short, long)
	}
}

// TestJSONScalarLinesStayOnTextPath: a line that is a bare number or a bare quoted string
// is ordinary text, and the text masker is the right tool for it.
func TestJSONScalarLinesStayOnTextPath(t *testing.T) {
	for _, raw := range []string{`42`, `"hello world"`, `true`, `null`} {
		want, _ := Templatize(Normalize(model.LogLine{Raw: raw}).Message)
		if got := pattern(raw); got != want {
			t.Errorf("scalar line %q = %q, want the text template %q", raw, got, want)
		}
	}
}

// TestTextPathUnchanged is the regression guard: this epic adds a JSON branch BEFORE the
// text path and must not disturb it. Every line here is templated exactly as it was in
// v0.6.0 — including lines that merely look like JSON.
func TestTextPathUnchanged(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "user 4821 failed", "user <NUM> failed"},
		{"level prefix stripped", "[ERROR] disk full", "disk full"},
		{"masker order", "from 192.168.1.1 id 550e8400-e29b-41d4-a716-446655440000", "from <IP> id <UUID>"},
		{"unterminated object", "{not json at all", "{not json at all"},
		{"trailing junk after object", `{"a":1} and then some`, "{<STR>:<NUM>} and then some"},
		{"looks like json, is not", `{a:1}`, "{a:<NUM>}"},
		// A coalesced trace is joined with "\n": it does not parse, so it stays on the
		// text path (the "panic:" prefix is stripped by Normalize, as it always was).
		{"multiline trace stays text", "panic: boom\n\tmain.go:42 +0x1d", "boom main.go:<NUM> +<HEX>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotHash := TemplatizeLine(jsonLine(tt.raw))
			if got != tt.want {
				t.Errorf("signature = %q, want %q", got, tt.want)
			}
			// The text path must be reached through the dispatcher, byte for byte.
			wantPattern, wantHash := Templatize(Normalize(model.LogLine{Raw: tt.raw}).Message)
			if got != wantPattern || gotHash != wantHash {
				t.Errorf("dispatcher diverged from Templatize: %q/%s vs %q/%s",
					got, gotHash, wantPattern, wantHash)
			}
		})
	}
}

// TestJSONShapeBounded: a pathological producer must cost bounded work and yield a bounded
// pattern — degrade, never hang. Deeply nested and very wide objects are both capped, and
// truncation is deterministic because keys are sorted first.
func TestJSONShapeBounded(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 200) + "1" + strings.Repeat("}", 200)
	wide := "{" + strings.Join(func() []string {
		fields := make([]string, 0, 10000)
		for i := range 10000 {
			fields = append(fields, strconv.Quote("k"+strconv.Itoa(i))+":1")
		}
		return fields
	}(), ",") + "}"

	done := make(chan struct{})
	var deepPattern, widePattern string
	go func() {
		defer close(done)
		deepPattern = pattern(deep)
		widePattern = pattern(wide)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("templating a pathological object did not terminate")
	}

	if !strings.HasSuffix(deepPattern, "{...}"+strings.Repeat("}", maxJSONDepth)) {
		t.Errorf("deep nesting not truncated at the depth cap: %q", deepPattern)
	}
	if !strings.HasSuffix(widePattern, ",...}") {
		t.Errorf("wide object not truncated at the node cap: %q", widePattern)
	}
	if n := strings.Count(widePattern, ":"); n > maxJSONNodes {
		t.Errorf("wide object rendered %d fields, cap is %d", n, maxJSONNodes)
	}
	// Truncation is deterministic, not whatever the map iteration happened to yield.
	if again := pattern(wide); again != widePattern {
		t.Error("truncation is not deterministic across runs")
	}
}

// TestJSONShapeDeterministic: the signature must not depend on map iteration order.
func TestJSONShapeDeterministic(t *testing.T) {
	raw := `{"z":1,"a":{"n":null,"m":[1,"x"]},"k":"v","b":true}`
	first := pattern(raw)
	for range 50 {
		if got := pattern(raw); got != first {
			t.Fatalf("signature varies between calls: %q vs %q", got, first)
		}
	}
}

// --- The §5.1 path must keep working ------------------------------------------------

// TestJSONLevelStillExtracted: templating a JSON line by shape must not cost it the
// level and message Normalize pulls out, which is what feeds severity scoring and the
// LLM context.
func TestJSONLevelStillExtracted(t *testing.T) {
	tests := []struct {
		raw         string
		wantLevel   string
		wantMessage string
	}{
		{`{"level":"error","msg":"boom"}`, "ERROR", "boom"},
		{`{"LEVEL":"FATAL","MSG":"boom"}`, "FATAL", "boom"},
		{`{"Severity":"warn","Message":"slow"}`, "WARN", "slow"},
		{`{"Lvl":"info","Text":"ready"}`, "INFO", "ready"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := jsonLine(tt.raw)
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
		})
	}
}

// TestJSONWithoutLevelHasNoSeveritySignal: the MCP case. No level field means no severity
// signal — which is correct, not a gap — and the line is templated by shape without
// tripping over the missing fields.
func TestJSONWithoutLevelHasNoSeveritySignal(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":7,"method":"notifications/message"}`
	line := jsonLine(raw)
	if line.Level != "" {
		t.Errorf("Level = %q, want empty: there is no level field to find", line.Level)
	}
	if line.Message != raw {
		t.Errorf("Message = %q, want the raw JSON fallback", line.Message)
	}

	p := New(score.New(scoringConfig(), nil))
	ev := p.Process(model.LogLine{Raw: raw}, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	if ev.Pattern != `{"id":<NUM>,"jsonrpc":<STR>,"method":<STR>}` {
		t.Errorf("Pattern = %q", ev.Pattern)
	}
	for _, r := range ev.Reasons {
		if strings.HasPrefix(r, "level ") {
			t.Errorf("a severity reason appeared without a level field: %v", ev.Reasons)
		}
	}
}

// TestJSONFatalStillScoresSeverity is the end-to-end guard on the §5.1 path: a JSON FATAL
// line still carries its level into the scorer and still earns the severity weight.
func TestJSONFatalStillScoresSeverity(t *testing.T) {
	p := New(score.New(scoringConfig(), nil))
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	ev := p.Process(model.LogLine{Raw: `{"level":"fatal","msg":"database is on fire"}`}, base)
	if ev.Line.Level != "FATAL" {
		t.Fatalf("Level = %q, want FATAL", ev.Line.Level)
	}
	if !ev.Escalate {
		t.Errorf("a JSON FATAL line did not escalate: score %.2f, reasons %v", ev.Score, ev.Reasons)
	}
	severity := false
	for _, r := range ev.Reasons {
		if strings.HasPrefix(r, "level FATAL") {
			severity = true
		}
	}
	if !severity {
		t.Errorf("no severity reason on a JSON FATAL line: %v", ev.Reasons)
	}
}

// TestJSONDedupThroughPipeline: the whole point, seen from the outside. Two MCP-style
// lines that differ only in values are ONE template with a climbing count.
func TestJSONDedupThroughPipeline(t *testing.T) {
	p := New(nil)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	first := p.Process(model.LogLine{Raw: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`}, base)
	second := p.Process(model.LogLine{Raw: `{"jsonrpc":"2.0","id":2,"method":"tools/call"}`}, base.Add(time.Second))
	other := p.Process(model.LogLine{Raw: `{"jsonrpc":"2.0","id":3,"result":{"tools":[]}}`}, base.Add(2*time.Second))

	if first.Hash != second.Hash || second.Count != 2 {
		t.Errorf("same-shape lines did not dedup: %s x%d vs %s x%d",
			first.Pattern, first.Count, second.Pattern, second.Count)
	}
	if other.Hash == first.Hash {
		t.Errorf("a different shape collapsed into %q", first.Pattern)
	}
}
