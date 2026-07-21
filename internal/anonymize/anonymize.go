// SPDX-License-Identifier: Apache-2.0

// Package anonymize masks sensitive values in text destined for a REMOTE LLM backend and
// restores them in the response — a reversible, per-request, in-memory-only mapping.
//
// This is a SEPARATE concern from the pipeline's template masker (internal/pipeline):
// that one masks variable parts to build a stable signature and runs on every line; this
// one runs only at the LLM boundary, only when the operator opts in, and its whole purpose
// is that a distinct value gets a stable, TYPE-TAGGED placeholder — <IP_1>, <HOST_2> — so
// the model still reasons about "an IP" or "a host that recurs" without seeing the literal.
//
// The two must not be merged: they answer different questions and one changing would
// silently change the other.
//
// Ordering matters for the same reason it does in the pipeline masker: an email contains a
// host, a URL contains a host and maybe credentials, an IP is digits-and-dots a host regex
// would grab. The more specific detector runs first (see detectorSpecs). Every detector's
// character classes exclude '<' and '>', so a placeholder like <USER_1> can never be
// re-matched by a later detector or by the fail-closed re-scan — that inertness is what
// keeps a home path in a Go stack trace from tripping the verifier (see Mask).
package anonymize

import (
	"fmt"
	"regexp"
	"strings"
)

// Mapper is the reversible mapping for ONE request. It is not safe for concurrent use:
// each escalation gets its own, discarded once the response is restored. Nothing here is
// ever persisted or shared across requests.
type Mapper struct {
	dets    []detector
	byValue map[string]string // original → placeholder, for stability within a request
	byToken map[string]string // placeholder → original, for Restore
	counts  map[string]int    // per-tag running counter, so IPs number independently of hosts
}

// New builds a Mapper. extraHostSuffixes extends the bare-hostname allowlist (detector 9b)
// for sites with their own private suffixes; with none, the shared default detectors are
// reused rather than recompiled.
func New(extraHostSuffixes ...string) *Mapper {
	dets := baseDetectors
	if len(extraHostSuffixes) > 0 {
		dets = buildDetectors(extraHostSuffixes)
	}
	return &Mapper{
		dets:    dets,
		byValue: make(map[string]string),
		byToken: make(map[string]string),
		counts:  make(map[string]int),
	}
}

// Mask replaces every sensitive value in s with a type-tagged placeholder, recording the
// mapping so Restore can reverse it. The same value seen again — in this or a later Mask
// call on the same Mapper — yields the same placeholder.
//
// It then FAILS CLOSED: it re-scans its own output with the deterministic-shape detectors
// and returns an error if any sensitive value survived. The caller must treat that error as
// "do not send this payload" (see internal/llm/anonymizing.go) — a privacy feature that
// silently degrades to plaintext is worse than none.
//
// Honest scope: this re-scan guards against a detector-ordering or implementation bug — by
// construction it can only catch what the detectors already match. It is NOT a guarantee
// about arbitrary unknown data; free-text logs can carry anything.
func (m *Mapper) Mask(s string) (string, error) {
	out := s
	for _, d := range m.dets {
		out = m.apply(out, d)
	}
	if tag := m.residue(out); tag != "" {
		return "", fmt.Errorf("anonymize: a %s value survived masking", tag)
	}
	return out, nil
}

// Restore reverses every placeholder this Mapper issued. Placeholders the model echoed
// back but never received — or mangled — are left as literal text: better a stray "<IP_9>"
// on a card than a wrong value.
func (m *Mapper) Restore(s string) string {
	if len(m.byToken) == 0 {
		return s
	}
	out := s
	for token, original := range m.byToken {
		out = strings.ReplaceAll(out, token, original)
	}
	return out
}

// apply replaces detector d's target group everywhere in s. FindAllStringSubmatchIndex is
// used rather than ReplaceAllStringFunc because some detectors mask only a SUBMATCH (the
// user:pass inside a URL, the token after "Bearer") while leaving the surrounding context —
// the scheme, the host — intact for a later detector to handle.
func (m *Mapper) apply(s string, d detector) string {
	locs := d.re.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		start, end := loc[2*d.group], loc[2*d.group+1]
		if start < 0 {
			continue // this alternative did not capture the group
		}
		b.WriteString(s[prev:start])
		b.WriteString(m.placeholder(d.tag, s[start:end]))
		prev = end
	}
	b.WriteString(s[prev:])
	return b.String()
}

// placeholder returns the stable token for one original value, minting a new one on first
// sight and reusing it thereafter — so a host that appears three times is <HOST_1> all
// three times, and the model can see it recur.
func (m *Mapper) placeholder(tag, original string) string {
	if p, ok := m.byValue[original]; ok {
		return p
	}
	m.counts[tag]++
	p := fmt.Sprintf("<%s_%d>", tag, m.counts[tag])
	m.byValue[original] = p
	m.byToken[p] = original
	return p
}

// residue re-runs the verify-eligible detectors and returns the tag of the first sensitive
// value still present, or "" if the text is clean. The fuzzy host detectors are excluded:
// their job is best-effort coverage of bare hostnames, and a missed bare host must not mute
// an otherwise-safe escalation.
func (m *Mapper) residue(s string) string {
	for _, d := range m.dets {
		if !d.verify {
			continue
		}
		loc := d.re.FindStringSubmatchIndex(s)
		if loc != nil && loc[2*d.group] >= 0 {
			return d.tag
		}
	}
	return ""
}

// The placeholder type tags. TOKEN covers every secret shape (JWT, AWS key, Bearer/sk-,
// embedded credentials): the card and the model care that it is a secret, not which kind.
const (
	tagIP    = "IP"
	tagEmail = "EMAIL"
	tagToken = "TOKEN"
	tagUUID  = "UUID"
	tagHost  = "HOST"
	tagUser  = "USER"
)

// detector is one masking rule: a compiled pattern, the submatch group to replace (0 = the
// whole match), the tag its placeholder carries, and whether the fail-closed re-scan trusts
// it (deterministic shapes yes; fuzzy hostnames no).
type detector struct {
	tag    string
	re     *regexp.Regexp
	group  int
	verify bool
}

// baseDetectors is the default rule set, compiled once.
var baseDetectors = buildDetectors(nil)

// defaultHostSuffixes are PRIVATE / INFRA suffixes only. Public TLDs are deliberately
// absent for BARE hostnames: Go panics and goroutine dumps are full of github.com/...,
// golang.org/x/..., go.uber.org/zap, and since multi-line grouping a whole dump is ONE
// escalation — masking those degrades the explanation for no privacy gain, while
// db-01.acme.internal is where the sensitivity actually lives. Hosts inside URLs, emails,
// and connection strings are masked regardless of suffix by their own detectors.
var defaultHostSuffixes = []string{
	"internal", "local", "lan", "corp", "intranet", "svc", "private",
}

// buildDetectors assembles the ordered rule set. Everything except the bare-host detector
// is constant; only the bare-host suffix alternation varies with config.
func buildDetectors(extraHostSuffixes []string) []detector {
	suffixes := append(append([]string{}, defaultHostSuffixes...), extraHostSuffixes...)
	bareHost := regexp.MustCompile(
		`(?i)\b(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+(?:` + strings.Join(escapeAll(suffixes), "|") + `)\b`)

	return []detector{
		// 1. JWT — three base64url segments; the header always starts "eyJ".
		{tagToken, regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), 0, true},
		// 2. AWS access-key id — a fixed, distinctive prefix.
		{tagToken, regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0, true},
		// 3a. Bearer token — mask the credential, not the word "Bearer".
		{tagToken, regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]+)`), 1, true},
		// 3b. sk- style API keys (OpenAI and friends).
		{tagToken, regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}\b`), 0, true},
		// 4. Credentials embedded in a URL / connection string: scheme://user:pass@host.
		//    Runs before host and email so "@host" survives for the host detector and
		//    "user:pass@host" is not misread as an email.
		{tagToken, regexp.MustCompile(`://([^:/@\s<>]+:[^@/\s<>]+)@`), 1, true},
		// 5. Email — before host, since an address contains a domain.
		{tagEmail, regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), 0, true},
		// 6. UUID — before IP/host; a fixed 8-4-4-4-12 shape.
		{tagUUID, regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), 0, true},
		// 7. IPv6 — before host and IPv4. The alternatives cover the :: elisions; forms
		//    without "::" and fewer than eight groups (times, MACs) do not match.
		{tagIP, regexp.MustCompile(ipv6Pattern), 0, true},
		// 8. IPv4 — validated octets, and a leading \b so v1.2.3.4 (a version) is left alone.
		{tagIP, regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\b`), 0, true},
		// 9a. Host inside a URL/URI authority — masked regardless of TLD. The optional
		//     userinfo run skips a (possibly already-masked) credential to reach the host.
		{tagHost, regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://(?:[^/@\s]*@)?([a-z0-9.-]+)`), 1, false},
		// 9b. Bare hostname — only the private/infra suffixes above (fuzzy: verify off).
		{tagHost, bareHost, 0, false},
		// 10. Home directory — mask only the username segment, keeping the rest of the path
		//     so /home/<USER_1>/go/pkg/mod/... stays useful in a stack trace.
		{tagUser, regexp.MustCompile(`(?:/home/|/Users/)([^/<>\s:]+)`), 1, true},
	}
}

// ipv6Pattern is the standard comprehensive IPv6 matcher, covering the "::" elision forms
// and the loopback "::1". Kept as a named constant because it is unreadable inline.
const ipv6Pattern = `(?i)(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,7}:` +
	`|(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,5}(?::[0-9a-f]{1,4}){1,2}` +
	`|(?:[0-9a-f]{1,4}:){1,4}(?::[0-9a-f]{1,4}){1,3}` +
	`|(?:[0-9a-f]{1,4}:){1,3}(?::[0-9a-f]{1,4}){1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,2}(?::[0-9a-f]{1,4}){1,5}` +
	`|[0-9a-f]{1,4}:(?::[0-9a-f]{1,4}){1,6}` +
	`|:(?:(?::[0-9a-f]{1,4}){1,7}|:)`

// escapeAll quotes each suffix for safe interpolation into the bare-host alternation.
func escapeAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}
