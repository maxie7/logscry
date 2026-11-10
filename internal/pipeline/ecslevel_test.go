// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"math"
	"testing"
	"time"

	"github.com/maxie7/logscry/internal/model"
	"github.com/maxie7/logscry/internal/score"
)

// TestNormalizeECSNestedLevel pins the level ECS writes nested, "log": {"level": …}, which is
// the form Spring Boot's structured logging emits. The dotted "log.level" was always read; the
// nested one got no level, so an exception logged at ERROR on stdout scored on novelty alone.
//
// Exactly that one shape is read: a top-level object "log" holding a string "level", both keys
// in any case. It sits in log.level's slot of jsonLevelKeys, behind the dotted key, and the
// rule stays the one every JSON level key follows: the first key present with a string value
// decides, even when its token is not a level. Nothing else is walked.
func TestNormalizeECSNestedLevel(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantLevel   string
		wantMessage string
	}{
		{"issue probe", `{"log":{"level":"ERROR"},"message":"boom"}`, "ERROR", "boom"},
		{"nested ERROR", `{"@timestamp":"2026-11-10T10:00:00.000Z","log":{"level":"ERROR","logger":"com.example.orders.PaymentService"},"message":"payment declined"}`, "ERROR", "payment declined"},
		{"nested WARN", `{"log":{"level":"WARN","logger":"com.example.orders.Cache"},"message":"cache cold"}`, "WARN", "cache cold"},
		{"nested INFO", `{"log":{"level":"INFO","logger":"com.example.orders.Application"},"message":"started"}`, "INFO", "started"},
		{"keys in any case", `{"Log":{"LEVEL":"error"},"msg":"x"}`, "ERROR", "x"},
		{"non-string dotted key leaves it to the nested one", `{"log.level":null,"log":{"level":"error"},"message":"x"}`, "ERROR", "x"},

		// Both spellings present: the dotted key decides, so no line read today changes.
		{"dotted beats nested", `{"log.level":"INFO","log":{"level":"ERROR"},"message":"x"}`, "INFO", "x"},
		{"earlier key beats nested", `{"level":"warn","log":{"level":"error"},"message":"x"}`, "WARN", "x"},

		// A non-object "log" or a non-string level reads as absent.
		{"log is a string", `{"log":"error","message":"x"}`, "", "x"},
		{"log is an array", `{"log":["error"],"message":"x"}`, "", "x"},
		{"log is a number", `{"log":3,"message":"x"}`, "", "x"},
		{"log is null", `{"log":null,"message":"x"}`, "", "x"},
		{"nested level is a number", `{"log":{"level":3},"message":"x"}`, "", "x"},
		{"nested level is null", `{"log":{"level":null},"message":"x"}`, "", "x"},
		{"nested level is an object", `{"log":{"level":{"name":"error"}},"message":"x"}`, "", "x"},
		{"nested level is a bool", `{"log":{"level":true},"message":"x"}`, "", "x"},
		{"unknown nested token", `{"log":{"level":"verbose"},"message":"x"}`, "", "x"},

		// No general walker and no other schemas.
		{"deeper nesting", `{"log":{"log":{"level":"error"}},"message":"x"}`, "", "x"},
		{"another parent", `{"event":{"level":"error"},"message":"x"}`, "", "x"},
		{"nested severity", `{"log":{"severity":"error"},"message":"x"}`, "", "x"},
		{"numeric level", `{"level":50,"message":"x"}`, "", "x"},

		{"no message key falls back to raw", `{"log":{"level":"error"}}`, "ERROR", `{"log":{"level":"error"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(model.LogLine{Raw: tt.raw})
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
		})
	}
}

// firstStringKeyRows pin the rule the nested key joins: the first level key present with a
// string value decides, even when its token is not a level, and no later key is consulted.
var firstStringKeyRows = []struct{ name, raw string }{
	{"unknown top-level token", `{"level":"verbose","severity":"error","message":"x"}`},
	{"unknown dotted token", `{"log.level":"verbose","log":{"level":"error"},"message":"x"}`},
}

func TestNormalizeFirstStringLevelKeyDecides(t *testing.T) {
	for _, tt := range firstStringKeyRows {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(model.LogLine{Raw: tt.raw}).Level; got != "" {
				t.Errorf("Level = %q, want none: the first string key decides", got)
			}
		})
	}
}

// TestNestedECSTemplateDoesNotMove: the level is read, the template is not touched. A JSON
// line is templated on its key structure, which never looked at the level keys.
func TestNestedECSTemplateDoesNotMove(t *testing.T) {
	raw := `{"log":{"level":"ERROR","logger":"com.example.orders.PaymentService"},"message":"payment 4242 declined"}`
	pattern, _ := TemplatizeLine(Normalize(model.LogLine{Raw: raw}))
	const want = `{"log":{"level":<STR>,"logger":<STR>},"message":"payment <NUM> declined"}`
	if pattern != want {
		t.Errorf("pattern = %s, want %s", pattern, want)
	}
}

// TestNovelNestedECSErrorEscalates is #168 end to end. Each line runs in a fresh pipeline, so
// each is novel: the nested twins share one template. The dotted ERROR is the harness check —
// it escalated before the fix too — so the nested ERROR fails on the level and nothing else.
func TestNovelNestedECSErrorEscalates(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantLevel string
		wantScore float64
		escalate  bool
	}{
		{"dotted ERROR", `{"log.level":"ERROR","log.logger":"com.example.orders.PaymentService","message":"payment declined"}`, "ERROR", 1.05, true},
		{"nested ERROR", `{"log":{"level":"ERROR","logger":"com.example.orders.PaymentService"},"message":"payment declined"}`, "ERROR", 1.05, true},
		{"nested INFO", `{"log":{"level":"INFO","logger":"com.example.orders.PaymentService"},"message":"payment declined"}`, "INFO", 0.45, false},
	}
	base := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(score.New(scoringConfig(), nil))
			ev := p.Process(model.LogLine{Raw: tt.raw, Stream: model.Stdout, Source: "stdin"}, base)
			if ev.Line.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", ev.Line.Level, tt.wantLevel)
			}
			if math.Abs(ev.Score-tt.wantScore) > 1e-9 {
				t.Errorf("score = %.2f, want %.2f (reasons %v)", ev.Score, tt.wantScore, ev.Reasons)
			}
			if ev.Escalate != tt.escalate {
				t.Errorf("Escalate = %v, want %v", ev.Escalate, tt.escalate)
			}
		})
	}
}

// BenchmarkNormalizeJSON measures the JSON level probe: the nested-ECS read must cost a line
// nothing unless no level key matched and "log" is present.
func BenchmarkNormalizeJSON(b *testing.B) {
	lines := []struct{ name, raw string }{
		{"leveled", `{"time":"2026-11-10T10:00:00Z","level":"info","msg":"order placed","order":"o-4242","user":"u-17"}`},
		{"levelless", `{"time":"2026-11-10T10:00:00Z","msg":"order placed","order":"o-4242","user":"u-17"}`},
		{"ecs-dotted", `{"@timestamp":"2026-11-10T10:00:00.000Z","log.level":"ERROR","log.logger":"com.example.orders.PaymentService","message":"payment declined"}`},
		{"ecs-nested", `{"@timestamp":"2026-11-10T10:00:00.000Z","log":{"level":"ERROR","logger":"com.example.orders.PaymentService"},"message":"payment declined"}`},
	}
	for _, l := range lines {
		b.Run(l.name, func(b *testing.B) {
			line := model.LogLine{Raw: l.raw}
			b.ReportAllocs()
			for b.Loop() {
				_ = Normalize(line)
			}
		})
	}
}
