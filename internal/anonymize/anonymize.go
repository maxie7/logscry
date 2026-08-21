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
// would grab. The more specific detector runs first (see buildDetectors). No detector's TARGET
// GROUP can land on a placeholder this package minted — <USER_1>, <TOKEN_2> — so masking is
// safe to run sequentially, the fail-closed re-scan cannot fire on its own output, and Restore's
// single non-fixpoint pass is correct; that inertness is what keeps a home path in a Go stack
// trace from tripping the verifier (see Mask).
//
// It is stated on the GROUP rather than on the whole match because that is what the code
// depends on and because the looser form would forbid a detector that is fine. apply writes
// only the group; residue tests only whether the group captured. Two detectors — the credential
// fallbacks 4b and 4c — read one of our tags on purpose, as context, and are safe (see ourPH).
//
// That same inertness is what MAKES the failure below possible, and the two are one fact seen
// from two sides. A detector whose span contains a value another detector already masked cannot
// step over the placeholder, so it does not match at all, and the rest of its span goes out in
// the clear. That is #46. Ordering does not fix it — the blocking detectors are the more
// specific ones and must run first — so the answer is a narrow fallback per blocked half rather
// than a looser class.
//
// The one deliberate exception is the pipeline's own placeholders: see pipelinePH.
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
// Honest scope, and it is narrower than it looks — narrower than this note itself claimed
// until #46, which is worth saying plainly rather than quietly correcting. The note used to
// read "catches a detector that missed a value ENTIRELY". #46 was exactly that: detector 4
// missed a URL credential entirely, and the re-scan reported clean. The claim was wrong as
// written, not merely incomplete.
//
// The correct claim has a precondition. This re-scan catches a detector that missed a value
// entirely — a raw 10.0.0.5 the ordering skipped — PROVIDED no earlier detector has already
// rewritten part of that value's span. If one has, the leftover is a shape the blocked detector
// does not match, and the re-scan reads it as clean for the same structural reason a partial
// match does. Interference between detectors and partial matching are one blindness with two
// causes, and the audit in BACKLOG.md enumerates which detector pairs can produce it.
//
// It equally CANNOT catch a detector that matched a value INCOMPLETELY, because the leftover
// of a partial match is by
// construction a shape the detector no longer matches: mask 2001:db8::dead:beef down to
// 2001:db8:: and the surviving "dead:beef" carries no "::" and fewer than eight groups, so
// the very detector that produced it reads it as clean. That was #41, live in every release
// from v0.4.0 to v0.8.5. Nothing at runtime rescues an under-matching pattern — only the
// pattern and the mode it is compiled in (see mustLongest). It is also NOT a guarantee about
// arbitrary unknown data; free-text logs can carry anything.
//
// #43 NARROWS that note by one class rather than widening it. A value the pipeline's template
// masker had already rewritten used to be invisible here for the same structural reason — the
// detector did not match hunter<NUM> at all, so the re-scan read it as clean. Every detector
// now recognizes its value in that form too, so for the verify-eligible ones a templatized
// credential, address or home path that survived masking IS caught on the re-scan. The
// blindness that remains is specifically "matched incompletely", not "arrived reshaped".
//
// #46 NARROWS it again by one more class, on the same terms. The credential fallbacks 4b and 4c
// are verify-eligible and their userinfo context admits our own tags, so a credential half left
// behind by a blocked detector 4 — the one shape this class actually produced — now trips the
// re-scan and the escalation is skipped rather than sent. What is NOT fixed in general is the
// class: any composite detector can still be blocked by a placeholder inside its span, and the
// cases the audit found and left open are filed rather than closed — a UUID inside a hostname
// (#48) and an IPv4-mapped IPv6 address (#49). Fallbacks were added where the leak was credential
// material; the audit table in BACKLOG.md, not this comment, is the record of what remains.
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

// danglingPlaceholder matches an INCOMPLETE placeholder left at the very end of a string —
// "<", "<IP", "<IP_1" — the shape produced when text is cut off mid-token.
var danglingPlaceholder = regexp.MustCompile(`<[A-Z]*(?:_\d*)?$`)

// TrimDanglingPlaceholder removes a partial placeholder from the end of s.
//
// It exists for salvaged answers only. A field that decoded as complete JSON cannot end
// mid-placeholder — its closing quote proves the value is whole — but a stream that died is
// salvaged through the prose fallback, which is raw text and can stop anywhere. Restoring
// that leaves the user reading "check <IP", a mangled fragment of a value we hold the real
// version of and simply failed to reach the end of.
func TrimDanglingPlaceholder(s string) string {
	return strings.TrimRight(danglingPlaceholder.ReplaceAllString(s, ""), " ")
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
//
// It sees whole values, not fragments, and the fragment can be produced two ways. A partially
// masked value leaves a remainder that no longer has the shape the detector looks for; so does a
// value whose span another detector already rewrote, which is #46 and is why "missed entirely"
// was never the safe half of the claim. Either way residue reports "clean" on precisely the
// failure a verifier is wanted for. TestMaskingIsComplete is the check that does see it, and
// TestNoDetectorGroupLandsOnOurOwnOutput guards the property both arguments rest on.
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

// mustLongest compiles pat in leftmost-longest mode. EVERY detector goes through here, and
// the rule has no exceptions on purpose.
//
// Go's default is leftmost-FIRST: the engine takes the first alternative that matches at the
// earliest position and stops. For a detector written as an alternation of shapes, that
// means the shortest listed form can beat the complete one — ipv6Pattern's
// "(?:[0-9a-f]{1,4}:){1,7}:" matched "2001:db8::" and left "dead:beef" in the clear (#41).
// Longest mode picks the complete match instead.
//
// It is applied to all thirteen rather than to the one detector known to need it, because
// which detectors need it is not a property anyone can keep true by inspection: IPv4 is also
// an alternation, and today it is saved only incidentally, by its literal "." separators and
// its trailing \b. A single compile site is what makes the mode impossible to forget when a
// detector is added. The cost of over-matching is one placeholder in a restatement that is
// already lossy; the cost of under-matching is disclosure. See the package comment.
//
// Submatches are unaffected here: leftmost-longest changes which parse wins only when a
// LONGER whole match exists by another route, and the four detectors that mask a submatch
// each have a single greedy capture with no such alternative.
func mustLongest(pat string) *regexp.Regexp {
	re := regexp.MustCompile(pat)
	re.Longest()
	return re
}

// buildDetectors assembles the ordered rule set. Everything except the bare-host detector
// is constant; only the bare-host suffix alternation varies with config.
func buildDetectors(extraHostSuffixes []string) []detector {
	suffixes := append(append([]string{}, defaultHostSuffixes...), extraHostSuffixes...)
	// A hostname label is a grammar rather than a flat run — it may not begin or end with a
	// hyphen — so tolerance goes on both halves of it rather than through tolerant().
	label, inner := `(?:[a-z0-9]|`+pipelinePH+`)`, `(?:[a-z0-9-]|`+pipelinePH+`)`
	bareHost := mustLongest(
		`(?i)\b(?:` + label + `(?:` + inner + `*` + label + `)?\.)+(?:` +
			strings.Join(escapeAll(suffixes), "|") + `)\b`)

	return []detector{
		// 1. JWT — three base64url segments; the header always starts "eyJ".
		{tagToken, mustLongest(
			`\beyJ` + tolerant(jwtChar) + `\.eyJ` + tolerant(jwtChar) + `\.` + tolerant(jwtChar)), 0, true},
		// 2. AWS access-key id — a fixed, distinctive prefix and exactly 16 more characters.
		{tagToken, mustLongest(
			`\b(?:AKIA|ASIA)(?:[0-9A-Z]{16}\b|` + mangled(`[0-9A-Z]`) + `)`), 0, true},
		// 3a. Bearer token — mask the credential, not the word "Bearer".
		{tagToken, mustLongest(`(?i)\bbearer\s+(` + tolerant(`[A-Za-z0-9._~+/=-]`) + `)`), 1, true},
		// 3b. sk- style API keys (OpenAI and friends).
		{tagToken, mustLongest(`\bsk-(?:[A-Za-z0-9]{16,}\b|` + mangled(`[A-Za-z0-9]`) + `)`), 0, true},
		// 4. Credentials embedded in a URL / connection string: scheme://user:pass@host.
		//    Runs before host and email so "@host" survives for the host detector and
		//    "user:pass@host" is not misread as an email. Both halves are placeholder-tolerant:
		//    the pipeline turns hunter2 into hunter<NUM>, and a run that stops at '<' can never
		//    reach the '@' that ends the credential, so the whole detector went quiet (#43).
		{tagToken, mustLongest(
			`://(` + tolerant(`[^:/@\s<>]`) + `:` + tolerant(`[^@/\s<>]`) + `)@`), 1, true},
		// 4b/4c. The half of a credential detector 4 can no longer reach.
		//
		//     Detector 4 masks "user:pass" as ONE value, which is what makes it readable as a
		//     credential rather than as two opaque runs -- and is also what breaks it. Detectors
		//     1-3b run first, so a JWT, an AKIA key or an sk- key on EITHER side of the colon
		//     mints a <TOKEN_n> inside detector 4's span; its classes exclude '<' by design, and
		//     with no second "://" to restart at the detector does not match AT ALL. The other
		//     half then goes out as literal text and residue() reports clean, because the
		//     detector that should have caught it is the one that was blocked (#46).
		//
		//     Two rules rather than one, because a single rule cannot mask two spans: 4b takes
		//     the password when the username was pre-masked, 4c takes the username when the
		//     password was. Neither can fire after detector 4 succeeded -- that leaves no ':'
		//     inside the userinfo -- and neither can fire after the other, because the half
		//     already masked begins with '<'.
		//
		//     THEY MASK ONLY THEIR OWN HALF, and that is not tidiness. Letting a group span our
		//     own tag would make the mapping nest (TOKEN_2 -> "<TOKEN_1>:password"), and nesting
		//     is not representable here: Restore iterates a Go map and substitutes ONCE, with no
		//     fixpoint, so a nested mapping resolves correctly or does not depending on map
		//     iteration order. Measured at 175 wrong out of 200 restores in one process. Both
		//     groups therefore exclude '<' and '>' exactly as detector 4's do; only the opposite
		//     half, which is context, admits a tag.
		//
		//     The runs are '*' rather than '+', which closes a SECOND and unrelated gap: detector
		//     4 required at least one character on each side of the colon, so
		//     "redis://:password@host" -- the ordinary Redis DSN, Redis having had no username
		//     before 6.0 -- matched no credential detector at all. That much held on every host
		//     shape; whether anything actually LEFT the process did not, because the email
		//     detector took "password@cache.acme.internal" whole whenever the authority ended in
		//     a dotted alphabetic suffix. Bare host, loopback or address literal and it shipped.
		//     Same patch, different cause; see the empty-half rows in completenessRows.
		{tagToken, mustLongest(
			`://(?:[^:/@\s<>]|` + pipelinePH + `|` + ourPH + `)*:(` +
				tolerant(`[^@/\s<>]`) + `)@`), 1, true},
		{tagToken, mustLongest(
			`://(` + tolerant(`[^:/@\s<>]`) + `):(?:[^@/\s<>]|` + pipelinePH + `|` + ourPH + `)*@`), 1, true},
		// 5. Email — before host, since an address contains a domain. Both halves are
		//    placeholder-tolerant: a digit ANYWHERE on either side of the '@' is enough for the
		//    pipeline to split the address, and ordinary corporate addresses have digits, so
		//    this was the likeliest of #43's five to fire. The TLD needs no tolerance — a TLD
		//    has no digits for the pipeline to mask.
		//
		//    The leading \b is gone with the widening, and what it excluded is a finite set
		//    rather than a judgement call. A match begins either with a local-class character
		//    or with a pipeline placeholder. Beginning with a WORD character (letter, digit,
		//    '_'), \b could only fail where the character before is also a word character —
		//    itself in the class, so leftmost matching would have started there and the span is
		//    identical. So \b excluded exactly the four NON-word members of the class as a
		//    leading character (. % + -), which now get absorbed into the placeholder and are
		//    enumerated one row each in TestAcceptedOverMasking, and a local part the pipeline
		//    collapsed WHOLE — <HEX>@corp.example.com from a hashed address — which '<' being a
		//    non-word character made unmatchable, sending the entire address including its
		//    domain. That last one is a leak, not a cost, and it is why the assertion goes.
		{tagEmail, mustLongest(
			tolerant(`[A-Za-z0-9._%+-]`) + `@` + tolerant(`[A-Za-z0-9.-]`) + `\.[A-Za-z]{2,}\b`), 0, true},
		// 6. UUID — before IP/host; a fixed 8-4-4-4-12 shape.
		{tagUUID, mustLongest(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), 0, true},
		// 7. IPv6 — before host and IPv4. The alternatives cover the :: elisions; forms
		//    without "::" and fewer than eight groups (times, MACs) do not match.
		{tagIP, mustLongest(ipv6Pattern), 0, true},
		// 8. IPv4 — validated octets, and a leading \b so v1.2.3.4 (a version) is left alone.
		{tagIP, mustLongest(`\b(?:(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\b`), 0, true},
		// 9a. Host inside a URL/URI authority — masked regardless of TLD. The optional
		//     userinfo run skips a (possibly already-masked) credential to reach the host.
		//     Placeholder-tolerant: without it the host stopped at its first <NUM> and
		//     api-gw7.prod.acme.com sent ".prod.acme.com" in the clear, with no second detector
		//     behind it — a public suffix is not on the bare-host allowlist (#43).
		{tagHost, mustLongest(
			`(?i)\b[a-z][a-z0-9+.-]*://(?:[^/@\s]*@)?(` + tolerant(`[a-z0-9.-]`) + `)`), 1, false},
		// 9b. Bare hostname — only the private/infra suffixes above (fuzzy: verify off).
		//     Tolerant for the same reason: db01.corp.internal masked only "corp.internal" and
		//     sent "db", and a digit in a middle label costs every label before it.
		//
		//     BOTH host detectors stay verify:false, and #43 does not change that. Flipping
		//     them would not have caught this class anyway — the leftover of a partial match,
		//     db<NUM>.<HOST_1>, is not a shape (?:label\.)+suffix matches, which is #41's
		//     structural blindness. And for 9b, missing a value ENTIRELY is the designed
		//     behaviour: bare public hostnames are deliberately left alone, so verify-eligible
		//     would fail closed on every escalation carrying api.example.com and mute exactly
		//     the stack traces this feature protects.
		{tagHost, bareHost, 0, false},
		// 10. Home directory — mask only the username segment, keeping the rest of the path
		//     so /home/<USER_1>/go/pkg/mod/... stays useful in a stack trace. Tolerant, and the
		//     awkward position is the one worth naming: a TRAILING digit was always harmless
		//     (/home/maxie7 → /home/<USER_1><NUM> loses nothing), while a digit in the MIDDLE
		//     split the value and sent the tail — /home/deploy2prod leaked "prod" (#43).
		{tagUser, mustLongest(`(?:/home/|/Users/)(` + tolerant(`[^/<>\s:]`) + `)`), 1, true},
	}
}

// pipelinePH matches a placeholder minted by the PIPELINE's template masker
// (internal/pipeline: TS/UUID/IP/HEX/NUM/STR).
//
// An escalation's Template field has already been through that masker, so a secret reaches
// us shape-broken: sk-livekeyabcdefghij0123456789XYZ arrives as sk-livekeyabcdefghij<NUM>XYZ,
// AKIAIOSFODNN7EXAMPLE as AKIAIOSFODNN<NUM>EXAMPLE, and a JWT as a dot-separated run riddled
// with <NUM>. A detector anchored on an unbroken run of [A-Za-z0-9] misses all three and the
// surviving characters go over the wire in the clear.
//
// WHICH detectors need this is not a matter of taste, and it is the one durable statement to
// take away from #43: THE GAPS ARE EXACTLY THE DETECTORS WHOSE VALUES THE PIPELINE DOES NOT
// ALREADY COLLAPSE. UUIDs and IP addresses arrive as <UUID> and <IP> — one placeholder,
// nothing left to recognize, no gap possible. Every one of the other nine receives a damaged
// but not collapsed value and reads a pipeline placeholder as just another character of the
// run. Four were widened here at 2da31be; the remaining five (URL credentials, email, both
// host detectors, home directory) were left out and leaked until #43. That is a fact about
// today's templatizer rather than a property of anything, so it is enforced by tests running
// the REAL Templatize → Mask composition — see TestEveryUncollapsedDetectorHasATemplatizedRow,
// which re-derives the exemptions from the templatizer itself every run.
//
// This does not weaken the inertness invariant, but the argument for it is now in two parts
// rather than one, because five of the nine carry no distinctive prefix to lean on:
//
//   - Every widened class still excludes '<' and '>', so a run cannot ENTER a placeholder.
//   - The alternation names the pipeline's six UNTAGGED placeholders and requires '>'
//     immediately after the name, so it can never match this package's own numbered output —
//     <IP_1> and <TOKEN_1> have '_' where it demands '>' — and so cannot be stepped over as a
//     unit either.
//
// Neither sequential masking nor the fail-closed re-scan can therefore trip on a value already
// masked. TestPlaceholdersAreInert holds both halves of that to the fire, and the email
// detector is the case that needs both: '_' and '.' ARE in its local-part class, so a run
// could start inside "TOKEN_1" and only the '>' before the '@' stops it.
const pipelinePH = `<(?:TS|UUID|IP|HEX|NUM|STR)>`

// ourPH matches a placeholder THIS package minted -- <TOKEN_1>, <HOST_2>. It is disjoint from
// pipelinePH by construction, on the same '_'-vs-'>' distinction the inertness argument above
// already rests on: the pipeline's six names are untagged and carry no "_<n>".
//
// It appears in exactly one place -- the two one-sided credential fallbacks, 4b and 4c -- and
// there only as CONTEXT, never inside a target group. That distinction is the whole of the
// safety argument and it is worth stating precisely, because the invariant in the package
// comment is loose where it says no detector may MATCH a placeholder.
//
// What apply() and residue() actually depend on is narrower: apply writes only the target group,
// and residue tests only whether the target group captured. A detector whose GROUP can land on
// our own output breaks tag numbering, breaks Restore, and lets the fail-closed re-scan fire on
// its own output. A detector that READS one of our tags to find its bearings does none of that.
// TestNoDetectorGroupLandsOnOurOwnOutput holds the narrow claim against the real masked output
// of every completeness row.
const ourPH = `<[A-Z]+_\d+>`

// jwtChar is the base64url alphabet of a JWT segment.
const jwtChar = `[A-Za-z0-9_-]`

// tolerant builds a run of one or more units, where a unit is a character from class OR a
// whole pipeline placeholder. For detectors with no length requirement, that is the entire
// fix: the run simply steps over the pipeline's damage.
func tolerant(class string) string {
	return `(?:` + class + `|` + pipelinePH + `)+`
}

// mangled builds a run that contains AT LEAST ONE pipeline placeholder.
//
// It exists for the two detectors carrying a length requirement — {16,} after "sk-", exactly
// {16} after AKIA/ASIA — which a placeholder breaks by collapsing a whole digit run into one
// token. Rather than lower those thresholds (which would widen false positives on ordinary
// prose for every payload, masked or not), the shortened form is accepted only when the run
// provably came out of the pipeline masker. A false positive here costs one <TOKEN_n> in a
// template that is already a lossy restatement of the trigger line; a false negative leaks a
// live key.
func mangled(class string) string {
	unit := `(?:` + class + `|` + pipelinePH + `)`
	return unit + `*` + pipelinePH + unit + `*`
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
