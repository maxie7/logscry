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
// One fact IS read from the pipeline rather than restated here: how a configured bare-host suffix
// looks once the template masker has rewritten it (#105, see bareHostPattern). That is the
// pipeline's handwriting, which this package has to read, not a second masker; asking the
// pipeline for it means a change there moves the tolerance with it instead of leaving a copy
// behind.
//
// Ordering matters for the same reason it does in the pipeline masker: an email contains a
// host, a URL contains a host and maybe credentials, an IP is digits-and-dots a host regex
// would grab. The more specific detector runs first (see buildDetectors).
//
// That rule governs detectors which COMPETE FOR THE SAME SPAN, and the distinction is
// load-bearing rather than pedantic, because detector 6 runs out of sequence and #48 is the
// reason. In each pair above, both detectors can match the SAME text and the more specific one is
// the right answer. Detector 6 is in no such relation with either host detector: a UUID standing
// alone is unreachable by both — 9a requires a "://" and 9b requires a dotted name ending in a
// private suffix, and a UUID has neither a scheme nor a dot. The only text they contend for is a
// UUID that is a LABEL INSIDE a hostname, and there the host is the right answer, because the
// value being protected is the domain and not the identifier. So 6 runs AFTER 9b, and that is the
// specificity rule applied rather than broken: containment, not competition, and the container
// wins. The cost is that such a UUID comes back <HOST_n> rather than <UUID_n>, which is the cheap
// half of the trade — before the move the model got <UUID_n> AND the real domain in the clear, so
// the tag was never the thing being preserved.
//
// AN IP IS THE OPPOSITE CASE, and that is why the move does not generalise. https://10.0.0.5/x is
// a URL whose authority IS the address, so detectors 8 and 9a genuinely compete for that span and
// 8 must win or the tag says HOST when the value is an IP. But https://10.0.0.5.nip.io/x needs 9a
// to win, because there the address is a label inside the authority. No ordering of a linear chain
// satisfies both, since the right answer depends on what the matched span is PART OF and a chain
// has no notion of that. That half was filed as #54, with a pre-pass identifying the authority as
// the candidate.
//
// #54 WAS CLOSED BY A RULE, NOT AN ORDER, and not by the pre-pass. 9d matches only an address
// JOINED to hostname labels, so it is more specific than 8 on exactly the span they share and
// absent everywhere else: the address that IS the authority is still 8's. The pre-pass was
// measured on paper against the same cells and fell short where it was cheapest -- its bare
// authority is a private-suffix name, and "resolve 10.0.0.5.nip.io" is not one -- while costing a
// span-aware apply and residue. A post-pass gluing our own <IP_n> to its neighbours closed the
// raw cells but not the Template field, where the address is the pipeline's <IP>, and would have
// put a target group on our own output. 9d is that glue read on raw text one step earlier.
//
// #57 WAS CLOSED THE SAME WAY, and it is the case the issue said no pattern could reach. A key that is
// only PART of a value -- a userinfo half, a host-list member, a hostname label -- was tagged by 1, 2
// or 3b on its own, and the remainder could lie on either side of the tag, so no rule run AFTER them
// could cover it. Two rules run BEFORE them instead, 4d and 9e, each matching only a key glued to
// other text inside one value, and masking the value whole. A key that stands alone in its value is
// still the key detectors', so #46's reason for running them first stands, and no struct, apply or
// residue change was needed.
//
// #108 IS THE SAME RULE READ FROM THE OTHER END. A key glued to '_' or a letter is no key to 1, 2 or 3b,
// whose \b fails beside a word character, so in prose it was sent whole. Every rule that owns a value
// around such a key already masks the value whole, BECAUSE the key rules do not see the key; teaching
// them to would block those rules and send the rest. So rule 11 runs LAST, reads only text no rule
// claimed, and masks the key with the glue left as prose.
//
// #110 IS #57'S RULE FOR A BEARER VALUE. 1 and 2 run before 3a, so a key that was only part of a bearer
// value was tagged alone and the text glued to it was sent. 3c runs before them and masks such a value
// whole; moving 3a ahead of them would have closed the same cells and renumbered every line that carries
// a bearer token and a key elsewhere.
//
// #51 IS 3b'S ALPHABET, NOT ITS ORDER. A current sk- key carries '-' and '_' in its tail (sk-proj-,
// sk-svcacct-, sk-ant-api03-), and 3b wanted sixteen unbroken letters and digits straight after "sk-",
// so every such key was sent whole and residue() read it as clean. The tail is now a run of the key's
// alphabet carrying a witness (see skBody), and 4d, 9e and rule 11 read it through keyShapes as before.
// What that costs a reader: text glued after an sk- key by '_' or '-' is the key's, since it is the key's
// alphabet, and a userinfo half or a host-list member that is such a key is now a key half.
//
// #114 IS A RULE AT THE END, NOT A READER. Stripe, GitHub, Slack and Google secrets matched no detector
// and were sent whole, raw and on the template, with residue() reading the line as clean. Each format
// family is now one verify-eligible rule run after rule 11 (rule 12, see vendorKeys). Every rule that
// owns a value around such a key already masked it whole, so a rule run last reads only the text none of
// them claims -- prose, a query value, a JSON value, an env var -- and changes nothing they mask.
//
// No detector's TARGET GROUP can land on a placeholder this package minted — <USER_1>, <TOKEN_2> — so masking is
// safe to run sequentially, the fail-closed re-scan cannot fire on its own output, and no mapping
// Mask records nests; that inertness is what keeps a home path in a Go stack trace from tripping
// the verifier (see Mask). Restore no longer depends on it: since #50 it resolves a nested
// mapping by rule rather than by map order.
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
// THE CREDENTIAL GRAMMAR IS COVERED BY THREE RULES, and the number is argued rather than
// accumulated, because three gaps were found in that grammar one at a time and each by a
// different accident (#46; the empty-username case found while fixing it; #55). Since #55
// detector 4 is one group over the WHOLE userinfo — RFC 3986 §3.2.1 puts ':' inside the
// production rather than between two of them — so every arrangement of the colon is one rule's
// business and the delimiter stops being a thing rules are keyed on. What stays split is 4b and
// 4c, and there the reason is this struct rather than the grammar: the half left behind sits on
// the left for one and the right for the other, and a detector carries one group and one tag.
// Three rules is merged as far as the struct allows.
//
// THE '@' WENT THE SAME WAY AT #58. Every userinfo class excluded it as "the delimiter", which
// made the FIRST '@' structural; RFC 3986 §3.2 makes the LAST one structural, and net/url
// agrees. All four userinfo runs -- 4, 4b, 4c and 9a's context -- now admit '@', and detector 4
// carries the other half of the RFC's rule as context (authorityTail) so that a tag between a
// raw '@' and the real delimiter makes it decline rather than take a prefix. What was NOT
// changed is '?' and '#', which the same RFC paragraph also ends the authority at: bounding
// there was measured and reopens a leak, and those two belong with the raw '/' in #59's class.
//
// THE HOST LIST IS A FIFTH RULE, NOT A WIDER CLASS (#61). 9a masks one host and stops at the port
// colon and the comma; a replica-set or failover authority -- host[:port](,host[:port])* -- sent
// every host after the first. The measured reason the list is a companion (9c) rather than a wider
// group on 9a is a shape masked today: a comma-separated list of URLs, where a group admitting a
// comma member eats the next URL's scheme and orphans its host. RE2 cannot refuse that member by
// lookahead, and a required trailing context on a FIRST-PASS rule narrows what it already masks;
// on a companion it costs nothing, because the companion's no-match is today's behaviour. So 9c
// reads the first member's tag as context, as 4b/4c read a credential half, and is the fifth rule
// to read one of our tags on purpose. Since #67 its group is ONE member and Mask re-applies it until
// nothing changes, so each pass re-anchors on the last tag in the list whichever rule minted it, and
// every member is its own placeholder; it is the one rule that iterates (see applyToFixpoint).
//
// AN UNDERSCORE IS A HOST CHARACTER (#66), and that is the fifth kind of cause again, its second
// instance: no host class admitted '_', which RFC 3986 puts in unreserved and logs put in Docker
// container names, Kafka brokers and internal hosts, so every host rule stopped at the first one and
// sent the rest. The email detector's domain class had the same gap. The classes widen by one
// character each; the one class that does NOT is listEnd, where '_' stays a terminator because
// there it decides what 9c falls back to, and the fallback is today's output (see listEnd).
//
// The one deliberate exception is the pipeline's own placeholders: see pipelinePH.
package anonymize

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/maxie7/logscry/internal/pipeline"
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
// reused rather than recompiled. Each suffix is read as written and as the pipeline rewrites
// it (k8s and k<NUM>s), so a host on it is masked on the Template field too (#105). A suffix is
// taken as given: internal/config strips its dots and refuses an empty one.
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
// class: any composite detector can still be blocked by a placeholder inside its span.
//
// #48 CLOSES ONE MORE, and it is the one ordering could reach. A UUID inside a hostname is masked
// as part of the host now, because detector 6 moved behind both host detectors — see the ordering
// note above for why that is the specificity rule applied rather than broken. What remains of that
// pair was the case ordering CANNOT reach: an IPv4 literal in a host label, where
// https://10.0.0.5.nip.io/x sent ".nip.io" and worker-10.0.0.5.corp.internal sent "worker-",
// because the two IPv4 cases want opposite orders and no linear chain gives both. Filed as
// #54, alongside an IPv4-mapped IPv6 address (#49). Fallbacks were added where the leak was
// credential material; the audit table in BACKLOG.md, not this comment, is the record of what
// remains.
//
// #54 CLOSES IT WITHOUT A PROVISO here. The host rules stay verify:false and so does 9d, so the
// re-scan sees nothing new; what it saw before it still sees -- a raw address left inside a name
// is detector 8's, and mutes the escalation.
//
// #55 IS NOT ONE OF THESE NARROWINGS, and separating it from them is the point of saying so. It
// was not a detector blocked by a placeholder and not one matching in part: it was a SHAPE NO
// DETECTOR DESCRIBED — a userinfo with no password — so the re-scan could not have caught it
// however hard it looked, there being no pattern to look with. Closing it makes the class
// verify-eligible for the first time: a password-less userinfo that ever survives masking now
// mutes the escalation instead of being sent. What stayed invisible was the remainder of a half
// only PARTLY recognised by an earlier detector, for exactly #46's reason; that was #57, and it is
// closed in the chain rather than here: 4d is verify-eligible, so a glued userinfo that ever survives
// masking mutes, and the old leftover is a shape the re-scan still reads as clean but no longer meets.
//
// #61 IS NOT A NARROWING EITHER, and it adds no proviso to the note above because the re-scan was
// never in a position to see it: the host detectors are verify:false, and had they been eligible
// the leftover of a multi-host authority ("host2:27017,host3") is not a shape any of them matches
// on its own -- #41's structural reason from the re-scan's side. What separates it from #41 is the
// pattern's intent: 9a matched exactly what it was written for, and what it was written for was
// smaller than the value. Closing it (9c) changes nothing about what residue() can see.
//
// #108 WIDENS WHAT residue() SEES by one class. A key glued to a word character matched no detector,
// so the re-scan read it as clean for #55's reason -- no pattern to look with. Rule 11 is
// verify-eligible, so a raw glued key that ever survives masking mutes.
//
// #110 changes nothing here. Its leftover, "Bearer <TOKEN_1>.prod", is a shape the re-scan reads as clean,
// and it no longer meets it; a raw key left in a bearer value was always 1's or 2's, and mutes.
//
// #114 WIDENS WHAT residue() SEES by the vendor formats, for #108's reason: rule 12 is verify-eligible,
// so a raw Stripe, GitHub, Slack or Google secret that ever survives masking mutes.
func (m *Mapper) Mask(s string) (string, error) {
	out := s
	for _, d := range m.dets {
		if !d.fixpoint {
			out = m.apply(out, d)
			continue
		}
		var err error
		if out, err = m.applyToFixpoint(out, d); err != nil {
			return "", err
		}
	}
	if tag := m.residue(out); tag != "" {
		return "", fmt.Errorf("anonymize: a %s value survived masking", tag)
	}
	return out, nil
}

// Restore reverses every placeholder this Mapper issued. Placeholders the model echoed
// back but never received — or mangled — are left as literal text: better a stray "<IP_9>"
// on a card than a wrong value.
//
// It is ONE pass over s by ourPHRe, and a token whose original itself contains a token of ours
// is expanded recursively (see resolve). Until #50 it ranged over byToken — a Go map — with one
// ReplaceAll per token and no fixpoint, so a nested mapping resolved correctly or did not
// depending on iteration order (measured wrong in 861-882 of 1000). One pass also means a
// substituted value is never re-read together with the text beside it, so a replacement cannot
// combine with its neighbours into a token that was never there.
//
// Mask produces no nested mapping today (TestRestoreHasNoNestedMapping), so for every input it
// can produce this returns what the old loop did: every token we mint matches ourPH whole, and
// two ourPH matches cannot overlap, so the scan visits exactly the occurrences ReplaceAll did.
func (m *Mapper) Restore(s string) string {
	if len(m.byToken) == 0 {
		return s
	}
	return ourPHRe.ReplaceAllStringFunc(s, func(tok string) string {
		if v, ok := m.resolve(tok, nil); ok {
			return v
		}
		return tok
	})
}

// resolve returns tok's original with every token of ours inside it expanded, or false if tok is
// unknown or its expansion is cyclic.
//
// path is the STACK of tokens currently being expanded: a token is pushed for its own expansion
// and popped when it returns, so a token repeated inside one value, or reached twice through a
// diamond, resolves fully. Re-entering a token still on the stack is a cycle, and the bound
// follows: the stack holds distinct tokens, so the depth never exceeds len(byToken), and no
// acyclic nesting can reach it.
//
// A cycle fails the WHOLE expansion and the outermost token is left verbatim. A cyclic mapping
// has no value, and any partial unrolling of it would be an invented string shown as a real one,
// which is the one thing Restore's contract forbids. A token-shaped string inside a value that is
// NOT in byToken is literal text of the original and is kept, not a failure.
func (m *Mapper) resolve(tok string, path map[string]bool) (string, bool) {
	v, ok := m.byToken[tok]
	if !ok || path[tok] {
		return "", false
	}
	if !ourPHRe.MatchString(v) {
		return v, true
	}
	if path == nil {
		path = make(map[string]bool)
	}
	path[tok] = true
	defer delete(path, tok)
	cyclic := false
	out := ourPHRe.ReplaceAllStringFunc(v, func(inner string) string {
		if _, known := m.byToken[inner]; !known {
			return inner
		}
		r, ok := m.resolve(inner, path)
		if !ok {
			cyclic = true
			return inner
		}
		return r
	})
	return out, !cyclic
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
//
// It is ONE pass for every detector. The one rule that needs more than one, 9c, is re-applied
// by applyToFixpoint, which calls this unchanged.
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

// maxHostListPasses is how many CHANGING passes applyToFixpoint allows before it gives up and
// fails closed. One pass masks the next raw member of EVERY list on the line, so what it bounds is
// the longest list, not the sum of them: two 40-member lists on one line take 39 passes, not 78.
//
// 64 is above MongoDB's 50-member replica-set limit, so no host list a driver accepts reaches it.
// What does reach it is a path-less URL followed by a long run of comma-separated cells, which 9c
// reads as a host list (TestAcceptedOverMasking). The cap exists for that shape: every pass scans
// the whole line, so an unbounded loop is O(cells x length), and anyone who can write a log line
// can make it take seconds.
const maxHostListPasses = 64

// applyToFixpoint re-applies d until a pass changes nothing, and is used for 9c alone.
//
// It TERMINATES, and the argument is short. A pass that changes s masks a raw run directly after
// a comma, and from then on that comma is followed by '<' for good: no rule rewrites a tag, and
// neither a tag nor the run it replaced contains a comma. So the passes that change anything are
// at most the commas in s, which per list is the number of members after the first.
//
// It FAILS CLOSED at the cap rather than returning what it has. A string that stopped settling
// still carries raw members, and sending it is the disclosure this rule exists to close; so the
// caller gets no string, Mask returns the error, and the escalation is skipped the way a residue
// hit skips it.
func (m *Mapper) applyToFixpoint(s string, d detector) (string, error) {
	for pass := 0; pass <= maxHostListPasses; pass++ {
		next := m.apply(s, d)
		if next == s {
			return s, nil
		}
		s = next
	}
	return "", fmt.Errorf("anonymize: a host list with more than %d members after the first was not masked", maxHostListPasses)
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

// The placeholder type tags. TOKEN covers every secret shape (JWT, AWS key, Bearer/sk-, a vendor
// key, embedded credentials): the card and the model care that it is a secret, not which kind.
const (
	tagIP    = "IP"
	tagEmail = "EMAIL"
	tagToken = "TOKEN"
	tagUUID  = "UUID"
	tagHost  = "HOST"
	tagUser  = "USER"
)

// detector is one masking rule: a compiled pattern, the submatch group to replace (0 = the
// whole match), the tag its placeholder carries, whether the fail-closed re-scan trusts it
// (deterministic shapes yes; fuzzy hostnames no), and whether Mask re-applies it until a pass
// changes nothing (one rule, 9c; see applyToFixpoint).
type detector struct {
	tag      string
	re       *regexp.Regexp
	group    int
	verify   bool
	fixpoint bool
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
// It is applied to all twenty-nine rather than to the one detector known to need it, because
// which detectors need it is not a property anyone can keep true by inspection: IPv4 is also
// an alternation, and today it is saved only incidentally, by its literal "." separators and
// its trailing \b. A single compile site is what makes the mode impossible to forget when a
// detector is added. The cost of over-matching is one placeholder in a restatement that is
// already lossy; the cost of under-matching is disclosure. See the package comment.
//
// Submatches are unaffected here: leftmost-longest changes which parse wins only when a
// LONGER whole match exists by another route, and the four detectors that mask a submatch
// each have a single greedy capture with no such alternative. Detector 4 is the one to check
// since #58, because its group is followed by context (authorityTail) and a class that admits
// '@' could in principle end the group at any of several '@'s. It cannot: the tail admits no
// '@' and must reach a terminator, so exactly one '@' -- the last before the authority ends --
// lets the whole pattern match, and the group's end is forced there. 4d carries the same group and
// the same tail, so the same argument holds; 9e's group ends its pattern, so its longest match is its
// longest group.
func mustLongest(pat string) *regexp.Regexp {
	re := regexp.MustCompile(pat)
	re.Longest()
	return re
}

// buildDetectors assembles the ordered rule set. Everything except the bare-host detector
// is constant; only the bare-host suffix alternation varies with config.
func buildDetectors(extraHostSuffixes []string) []detector {
	suffixes := append(append([]string{}, defaultHostSuffixes...), extraHostSuffixes...)
	bareHost := mustLongest(bareHostPattern(suffixes))

	dets := []detector{
		// 4d. A userinfo with a key GLUED inside one of its halves (#57). It runs HERE, ahead of the key
		//     detectors, and the out-of-sequence number is 6's and 9d's convention: it is a credential
		//     rule, and the audit table and the issues number those 4-4d.
		//
		//     Until #57, 1, 2 and 3b tagged a key that was only PART of a half on its own, and every
		//     credential group excludes '<', so the remainder was sent:
		//     s3://AKIA...EXAMPLE.prod:secret@bucket went out as s3://<TOKEN_1>.prod:<TOKEN_2>@<HOST_1>.
		//     4b and 4c could not reach it -- their half is the one the OTHER half's key left behind,
		//     and here the key and its remainder are one half, with the remainder on either side of the
		//     tag. Running 4 before the key detectors was rejected for #46 and that stands: a key that
		//     IS a half keeps its own tag. THIS IS NOT A REORDERING, it is #54's remedy: a rule more
		//     specific than the key detectors on exactly the span they share, a key with text beside it
		//     inside a half, and absent wherever a key stands alone in its half.
		//
		//     THE GROUP IS DETECTOR 4'S, the whole userinfo up to the delimiting '@', with authorityTail
		//     behind it. A key glued to other text cannot be a half on its own, so what #46 ran the key
		//     rules first to keep -- "this half is a key" -- was never there to keep, and the value goes
		//     to the rule that owns it. The halves are split at the first ':', as 4b and 4c split them,
		//     and a key is "glued" when its half carries a unit before it or a character after it that
		//     the key could not have continued with (see glued). The key is the key detectors' own
		//     pattern, \b and all, so a key 2 would not match is not one here either.
		//
		//     verify:true, as every credential rule: a glued userinfo that ever survives masking mutes
		//     the escalation. Its group reads a RAW key only, so it cannot fire on our own output.
		{tagToken, mustLongest(gluedUserinfo), 1, true, false},
		// 9e. A hostname with a key GLUED inside it (#57), the host-side twin of 4d: a key as a label
		//     (https://AKIA...EXAMPLE.prod.acme.com/x sent the domain), or a host-list member with text
		//     beside the key (mongodb://h1,AKIA...EXAMPLE.prod,host3/db sent ".prod" and ended 9c's list,
		//     sending host3). HOST, and the whole name is the value: containment, as for #48's UUID and
		//     #54's address -- what is protected is the domain, and the key is a label of it.
		//
		//     Its context is one "://", '@' or ',' directly before the name, and nothing after it:
		//     leftmost-longest makes the run maximal without a right edge, and a consumed one would eat
		//     the comma the next member needs, so two fused members side by side would not both match.
		//     It does not check that the name is in an authority, and the cost is pinned in
		//     TestAcceptedOverMasking: a key glued to labels after a comma or an '@' in prose is a host.
		//     A BARE name on a private suffix is not reached; that would mean reading 9b's configurable
		//     suffixes in a rule that runs before the key detectors (pinned as a residual).
		//
		//     It runs after 4d, so a glued userinfo is a TOKEN before this rule can read any of it as a
		//     host, and before 9a and 9c, so a fused member reaches 9c as a whole tag and the list
		//     re-anchors on it at no cost to the pass cap. A tag behind the '@' makes detector 4 decline,
		//     as authorityTail requires; 4b and 4c then take the halves, or 9a's fallback tags a
		//     password-less username HOST -- both main's output for that shape.
		//
		//     verify:false, as every host rule: a raw key it ever misses is still detector 2's.
		{tagHost, mustLongest(gluedHost), 1, false, false},
		// 3c. A bearer value with a key GLUED inside it (#110), 3a's value read before 1 and 2 can tag the key
		//     in it. The out-of-sequence number is 4d's and 9e's convention: it is a bearer rule.
		//
		//     Until #110, 1 or 2 tagged a JWT or an AWS key that was only PART of a bearer value, and 3a's
		//     group excludes '<', so it could not claim what was left: "Bearer AKIA...EXAMPLE.prod" went out
		//     as "Bearer <TOKEN_1>.prod", and on the template, where the mangled key has no closing \b, so did
		//     "_prod" and a lowercase letter. sk- was never affected, because 3b runs after 3a.
		//
		//     THIS IS NOT A REORDERING, and the reorder was measured. Running 3a before 1 and 2 closes the same
		//     cells, but it mints EVERY bearer value first, so a line with a bearer token and a key elsewhere
		//     swaps its placeholder numbers. This rule matches only a bearer value carrying a glued key, the
		//     span 3a and the key rules share, and is absent everywhere else: a key that IS the whole value is
		//     still 1's or 2's, and every other bearer value is still 3a's.
		//
		//     THE GROUP IS 3a'S, the whole bearer value. A key glued to other text there is part of one
		//     credential, and the value is the secret, as #57 decided for a userinfo. Glued means a unit of
		//     the value before the key, or after it a character the key could not have continued with (see
		//     glued). The key is 1's and 2's own pattern, \b and all, so where 2 sees no key -- "x_AKIA..." --
		//     3a still takes the value whole, as before.
		//
		//     It runs after 4d and 9e and is disjoint from both: their spans carry no whitespace, and a bearer
		//     value carries none of their anchors. verify:true, as every credential rule; a raw key left in a
		//     bearer value is still 1's or 2's on the re-scan.
		{tagToken, mustLongest(gluedBearer), 1, true, false},
		// 1. JWT — three base64url segments; the header always starts "eyJ".
		{tagToken, mustLongest(jwtKey), 0, true, false},
		// 2. AWS access-key id — a fixed, distinctive prefix and exactly 16 more characters.
		{tagToken, mustLongest(awsKey), 0, true, false},
		// 3a. Bearer token — mask the credential, not the word "Bearer".
		{tagToken, mustLongest(`(?i)\bbearer\s+(` + tolerant(bearerChar) + `)`), 1, true, false},
		// 3b. sk- style API keys (OpenAI and friends), '-' and '_' in the tail since #51 (see skBody).
		{tagToken, mustLongest(skKey), 0, true, false},
		// 4. Credentials embedded in a URL / connection string: scheme://userinfo@host.
		//    Runs before host and email so "@host" survives for the host detector and a userinfo
		//    is not misread as an email address. Placeholder-tolerant: the pipeline turns hunter2
		//    into hunter<NUM>, and a run that stops at '<' can never reach the '@' that ends the
		//    credential, so the whole detector went quiet on the Template field (#43).
		//
		//    THE DELIMITER IS THE LAST '@', NOT THE FIRST, and the class admits '@' to say so. That
		//    is #58. Until then every userinfo class excluded '@' on the reasoning that '@' is the
		//    delimiter, so "postgres://user:p@ss@db" was masked to the FIRST one, 9a read the "ss"
		//    behind it as an authority, and the real host went out in the clear; a raw '@' in the
		//    USERNAME sent the password as well. A raw '@' in a userinfo is illegal -- RFC 3986
		//    §3.2.1 wants %40 -- and ordinary, because people put '@' in passwords and paste DSNs
		//    into logs unencoded. It is not, however, ambiguous: §3.2 makes the delimiting '@' the
		//    last one before the authority ends, and net/url already parses it that way (measured:
		//    user="user" pass="p@ss" host="db", no error). This rule now reads the same string the
		//    same way the standard library does.
		//
		//    THE RULE IS WRITTEN IN FULL, and the second half is authorityTail. "Last '@'" on its
		//    own is what leftmost-longest gives a class that admits '@' -- except where a tag sits
		//    between a raw '@' and the real delimiter. "us@er:<TOKEN_1>@db" is that shape (#46's
		//    4c cell with a raw '@' in it), and a rule without the tail matched it as "us@" and
		//    sent "er" in the clear, where it used to be mis-tagged as a host: a host leak traded
		//    for a username-fragment leak, which is the wrong direction. The tail says what the RFC
		//    says: after the delimiting '@' comes host[:port] and then the end of the authority,
		//    with no further '@' in between. On the blocked shape that fails, this rule DECLINES,
		//    and 4c -- written for exactly a userinfo one of whose halves we already masked --
		//    takes the whole half. The parse is unique: the tail cannot contain '@', so the group's
		//    end is forced to the last '@' before a terminator, whatever the group could have
		//    matched on its own.
		//
		//    WHAT IS NOT BOUNDED IS '?' AND '#', and that was measured rather than left to symmetry.
		//    The same paragraph of the same RFC ends the authority at '/', '?' or '#'; this class
		//    excludes '/' and admits the other two, and it would have been tidy to stop admitting
		//    them alongside admitting '@'. It reopens a leak: "postgres://user:p?ss@db" is fully
		//    masked today and, bounded, sends "p?ss" and "db". The symmetry misleads because
		//    net/url cuts fragment and query BEFORE resolving the '@', so it errors on a raw '?',
		//    '#' and '/' in a password identically -- those three are one class, the class with no
		//    reliable parse, and it is #59's. Bounding at '?' and '#' would be the '/'-exclusion
		//    extended to its siblings, i.e. part of #59's remedy, whatever that turns out to be.
		//    What admitting '@' costs instead is two over-masks, both pinned in
		//    TestAcceptedOverMasking: a query carrying two '@'s is absorbed one parameter further,
		//    and a credentialed URL whose QUERY carries an '@' absorbs the host into the token.
		//
		//    ONE GROUP OVER THE WHOLE USERINFO, and the colon inside it is not structural. That is
		//    #55, and it is a MERGE rather than an addition. RFC 3986 §3.2.1 gives
		//    userinfo = *( unreserved / pct-encoded / sub-delims / ":" ) -- the colon is a MEMBER
		//    of the production, not a delimiter between two productions -- and this rule now says
		//    the same thing. Until #55 it was written as two runs with a literal ':' between them,
		//    which describes the same value in a way that left three of its arrangements
		//    unreachable, and they were found one at a time by three different accidents:
		//
		//      user:pass   both halves, the only shape the old rule was written for
		//      :pass       no username -- the ordinary Redis DSN, Redis having had none before 6.0
		//      user:       no password
		//      user        NO COLON AT ALL, so nothing to anchor on: postgres://appuser@db sent
		//                  "appuser" from v0.4.0 onward (#55)
		//
		//    A FOURTH RULE for the last of those was the obvious fix and was rejected. It closes
		//    exactly the same leaks as this merge -- measured against the whole existing corpus,
		//    not assumed -- so the choice was rule-set shape rather than coverage, and a grammar
		//    covered by four overlapping patterns keyed on which delimiters happen to be present
		//    is precisely how the first three gaps happened. Adding a fifth arrangement as a fifth
		//    pattern would entrench the shape that has already failed three times.
		//
		//    WHAT THE MERGE COSTS is a signal on the way OUT, and it is pinned in
		//    TestAcceptedOverMasking rather than left implicit in two moved completeness rows. A
		//    colon beside an ABSENT half is captured with the half that is there, so
		//    "redis://:<TOKEN_1>@" becomes "redis://<TOKEN_1>@". Restore is unaffected -- it
		//    substitutes the exact matched text -- but the leading colon used to tell the model
		//    that the missing half was the USERNAME, which is what makes a Redis DSN legible as
		//    one. The degenerate case is louder: "://:@host" now mints a placeholder for a lone
		//    delimiter, announcing a credential where there is none.
		//
		//    THE CLASS STAYS NEGATED rather than becoming RFC 3986's positive one
		//    ([A-Za-z0-9-._~%!$&'()*+,;=:]). A positive class would be NARROWER than this, and
		//    narrowing is the disclosure direction in this package; a password carrying an
		//    unencoded '?' or '#' is illegal and happens. Negation is also why percent-encoding
		//    needs no special handling and never did -- "%40" is three characters, none of which
		//    is '@' -- and two completeness rows now say so, because nothing tested it before #55.
		//
		//    '<' and '>' stay excluded. That is the inertness invariant rather than a detail: it
		//    is what keeps this group off our own output, and it is also the wall that a half only
		//    PARTLY recognised by an earlier detector runs into, where the remainder may lie on
		//    either side of the minted tag and no pattern run after the tag reaches it. That was
		//    #57, and 4d, run before the key detectors, is its remedy. It is also
		//    what makes the tail's "decline" work: a group that cannot start on or enter a tag has
		//    nowhere else to go once the tail refuses.
		{tagToken, mustLongest(`://(` + tolerant(`[^/\s<>]`) + `)@` + authorityTail), 1, true, false},
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
		//     own tag would put a target group on our own output, which the inertness invariant
		//     forbids, and would make the mapping nest (TOKEN_2 -> "<TOKEN_1>:password"). Until
		//     #50 nesting also made Restore resolve by map order; it now resolves by rule, so that
		//     second reason is gone and the first is the one that stands. Both groups therefore
		//     exclude '<' and '>' exactly as detector 4's do; only the opposite half, which is
		//     context, admits a tag.
		//
		//     The CONTEXT runs are '*' rather than '+' so a pre-masked half beside an absent one
		//     still resolves -- "://<TOKEN_1>:@host". The empty-half shapes themselves stopped
		//     being these rules' business at #55: detector 4 now spans the whole userinfo and
		//     covers "://:pass@" and "://user:@" directly, so what reaches 4b and 4c is only ever
		//     a userinfo one of whose halves THIS PACKAGE already masked.
		//
		//     Both admit '@' since #58, group and context alike, so the half they take runs to the
		//     LAST '@' as detector 4's does. They carry no authorityTail, and the reason is what a
		//     tag after a raw '@' in THEIR half would mean: the other half is already a tag by
		//     construction, so a second tag inside this one is a half an earlier detector
		//     recognised only in part -- #57's shape rather than #58's, and 4d's since #57.
		//
		//     THEY ARE ALSO WHY THE #55 MERGE STOPS AT THREE RULES, and the reason is the struct
		//     rather than effort. Detector 4 could absorb the colon-less case because the two
		//     differed only in whether ':' was in the class. These two cannot follow: the half to
		//     mask sits on the LEFT for one and on the RIGHT for the other, and a detector carries
		//     ONE group and ONE tag. Covering both from a single pattern needs two capture
		//     positions -- `group int` becoming a group list with a "first that captured"
		//     convention, plus matching changes in apply, residue, completenessSecretFor and
		//     TestNoDetectorGroupLandsOnOurOwnOutput. So three rules is merged as far as the
		//     struct allows, not nearly merged. The same wall made a PARTLY recognised half
		//     uncoverable by any pattern run after the tag (#57): there the remainder can lie on
		//     either side of it. Run before the tag exists, one rule covers it, and that is 4d.
		{tagToken, mustLongest(
			`://(?:[^:/\s<>]|` + pipelinePH + `|` + ourPH + `)*:(` +
				tolerant(`[^/\s<>]`) + `)@`), 1, true, false},
		{tagToken, mustLongest(
			`://(` + tolerant(`[^:/\s<>]`) + `):(?:[^/\s<>]|` + pipelinePH + `|` + ourPH + `)*@`), 1, true, false},
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
		//
		//    The domain class admits '_' since #66. Without it an address whose domain carried one
		//    matched nothing, no later rule reads a bare public domain, and the WHOLE address was
		//    sent, local part included. A URL's userinfo is detector 4's before this rule runs, so
		//    the widening reaches bare address shapes only -- an ssh-style user@host among them, and a
		//    systemd template unit whose instance carries a '_', which reads as an address exactly as
		//    one without a '_' always has (pinned in TestAcceptedOverMasking).
		{tagEmail, mustLongest(
			tolerant(`[A-Za-z0-9._%+-]`) + `@` + tolerant(`[A-Za-z0-9._-]`) + `\.[A-Za-z]{2,}\b`), 0, true, false},
		// 6 ran HERE until #48 and now runs after 9b — see the block below and the package
		//    comment. The numbering is the audit table's, not the chain's.
		// 7a. A "::"-led address glued on by ':' (#134). It runs HERE, directly before 7, and the
		//     number is 9d's convention: it is 7's rule, for one shape.
		//
		//     Until #134, 7 sent it. In a run of three colons, a 7 match that ends in "::" -- the bare
		//     "::" after a token, or "c::" after a hex-ending one -- starts one colon early and takes
		//     the first colon of the address's own "::". What is left, ":ffff:0:0" in
		//     peer:::ffff:0:0, has no "::" and fewer than eight groups, so nothing matches it and
		//     residue() reads it as clean: #41's blindness. No address contains three colons in a
		//     row, so ":::" is exactly where that cut happens.
		//
		//     THIS IS NOT A REORDERING, and the left end is why. 7a starts where 7 would: the groups
		//     before the run are 7's own (single colons, at most four hex characters each), and their
		//     count is unbounded only because 7 starts on the first of eight groups too (alternative
		//     1). Where they cannot reach further, what is left before them ends in "::", and 7 masks
		//     that as it always did. So 7a only moves the RIGHT end of a match 7 would have made.
		//
		//     THE RIGHT END IS BOUNDED, and that is the whole safety argument. After the groups comes
		//     CONTEXT from ipv6GluedRunEnd, which is not masked: whitespace, closing punctuation, '/',
		//     '%', a '.' that ends a sentence, or the end of the text. Where none follows, 7a does not
		//     match and 7 and everything after it run exactly as before. Each of those ends every value
		//     a later rule reads, or starts one outside the group (a home path's "/home/"). A '-', '_',
		//     a letter or any other '.' could each CONTINUE such a value -- an IPv4 address, a host, a
		//     URL scheme, a key -- and masking the groups would cut it: on peer:::ffff:192.168.1.1, 8
		//     masked the quad, and taking "192" would have sent ".168.1.1". Since #49 the groups may end
		//     in a dotted quad (ipv6DottedQuadTail, shared with 7b), so that address is one value, up to
		//     the same boundary. So 7a may mask more than 7,
		//     never cut what another rule would have masked; that is asserted over a corpus in
		//     TestGluedRunMasksASupersetOfMain, not only argued here. Those neighbours stay 7's, and
		//     what 7 sends beside them is the rest of #134's class.
		//
		//     The groups after the run read the pipeline's placeholders, so the Template field's
		//     :::ffff:<NUM>:<NUM> is one value as well (#43). The groups before it do not, so on the
		//     template too 7a starts where 7 does.
		//
		//     WHAT IT COSTS is pinned in TestAcceptedOverMasking: the gluing colon goes into the
		//     placeholder, so does a port written netstat's way (:::22 is "::" and ":22", and reads the
		//     same as ":" and "::22"), and a ':::' operator between spaces is one placeholder.
		//
		//     verify:true, as 7. Its group excludes our '<' and '>', so the mapping cannot nest.
		{tagIP, mustLongest(ipv6GluedRunPattern), 1, true, false},
		// 7b. An address whose last 32 bits are a dotted quad (#49). It runs HERE, after 7a and directly
		//     before 7, and the number is 7a's convention: it is 7's rule, for one shape.
		//
		//     Until #49, 7 sent it. RFC 3986 lets the last 32 bits of any IPv6 form be written as an IPv4
		//     address -- ::ffff:10.0.0.5, ::10.0.0.5, ::ffff:0:10.0.0.5, 64:ff9b::10.0.0.5,
		//     0:0:0:0:0:ffff:10.0.0.5 -- and 7 has no such form. It took the groups and the first octet,
		//     a legal hex group, and stopped at the dot; 8 cannot read three octets, so
		//     "peer ::ffff:10.0.0.5 down" was sent as "peer <IP_1>.0.0.5 down" and residue() read it as
		//     clean: #41's blindness. The full form lost its groups instead, to 8 taking the quad alone.
		//
		//     THIS IS NOT A REORDERING, and the left end is why. The groups before the "::" are 7's own,
		//     unbounded in count as 7a's are, so 7b starts where 7 would; bounded to the RFC's counts it
		//     would start later than 7 on a longer prefix and send what 7 masks. The full form wants six
		//     groups or more, so a quad after a word and one colon ("src:10.0.0.5") stays 8's. The groups
		//     are raw hex only, so on the Template field too 7b starts where 7 does.
		//
		//     THE RIGHT END IS BOUNDED, by 7a's boundary and for 7a's reason. Placed inside 7 the forms
		//     would end at the quad wherever it stopped, and the character after it can continue a value
		//     a later rule reads: on "::ffff:10.0.0.5.6", 7 then 8 mask "::ffff:10" and "0.0.5.6", and
		//     ending at the quad sends ".6"; a private name or an address glued on by ':' would lose
		//     the character that joins it. With the boundary, 7b matches only where a character no later
		//     value contains follows, and everywhere else 7 and the rest run as before. That is asserted
		//     against v0.12.24 in TestDottedQuadMasksASupersetOfV01224. What it leaves -- a '-', '_', a
		//     letter or a dot inside a word after the quad -- is #137's, as for 7a.
		//
		//     7a TAKES THE SAME RIGHT END, through ipv6DottedQuadTail, and must. On a glued address 7a
		//     declines, 7b starts one colon later and the gluing colon is sent, which 7's bare "::" masks.
		//     So 7b runs after 7a, and the two cannot disagree about where an address ends.
		//
		//     WHAT IT COSTS is pinned in TestAcceptedOverMasking: an unbracketed port after the quad goes
		//     into the placeholder, because ':' is not a boundary and the rule could not fire otherwise
		//     (after a dotted quad nothing hex can follow, so the port is not ambiguous; it is lost all the
		//     same); the hex character of a hex-ending token goes with the address, as on 7; and a prefix
		//     longer than an address is one value with the quad. On the Template field most of these forms
		//     arrive as <IP>.<NUM>.<NUM>, with nothing of the value left, which this rule does not read:
		//     reading it would cut a name 9d masks there (db.<IP>.<NUM>.<NUM>).
		//
		//     verify:true, as 7. Its group admits the pipeline's <IP> and none of our tags, so the mapping
		//     cannot nest.
		{tagIP, mustLongest(ipv6DottedQuadPattern), 1, true, false},
		// 7. IPv6 — before host and IPv4. The alternatives cover the :: elisions; forms
		//    without "::" and fewer than eight groups (times, MACs) do not match.
		{tagIP, mustLongest(ipv6Pattern), 0, true, false},
		// 9d. An IPv4 address that is a LABEL inside a hostname (#54). It runs HERE, before 8, and the
		//     out-of-sequence number is the same convention as 6's: it is a host rule, and the audit
		//     table, the README and the issues number host rules 9a-9d.
		//
		//     Until #54, 8 tagged the address on its own and every host class excludes '<', so the host
		//     rules were blocked behind the tag and the rest of the name was sent:
		//     https://10.0.0.5.nip.io/x sent ".nip.io", worker-10.0.0.5.corp.internal sent "worker-",
		//     and as a later list member the mixed member ended 9c's list. Running 9a before 8 is the
		//     impossibility #48 recorded -- https://10.0.0.5/x must stay <IP_1> -- and that stands.
		//     THIS IS NOT A REORDERING. It is a more specific rule: it matches only an address JOINED to
		//     hostname labels across '.' or '-', so it never competes with 8 for an address that stands
		//     alone, and every address 8 masked whole before, 8 still does. What is new is that the
		//     chain can now say what the address is part of, for the one shape where that matters.
		//
		//     THE SHAPE, part by part.
		//       the address:  ipv4Pattern, 8's own, or the pipeline's <IP> on the Template field --
		//                     where a bare name on a public suffix ("resolve <IP>.nip.io") was no host
		//                     rule's business and sent the labels around it.
		//       the labels:   runs of hostname characters on either side, joined to the address by '.'
		//                     or '-', each starting and ending on a label character -- so a sentence
		//                     dot or a trailing hyphen stays outside -- and placeholder-tolerant.
		//       a letter:     at least one side carries a letter outside the address. A DNS name's top
		//                     label is alphabetic, and nothing cheaper tells a name from an address
		//                     beside a number: "10.0.0.5.443" (BSD netstat's address.port), "10.0.0.5-20"
		//                     and "10.0.0.5-10.0.0.9" stay 8's.
		//       the edges:    CONTEXT, not group: the run starts at the beginning of the text or after
		//                     a non-label character, and ends at one or at the end. \b cannot stand in
		//                     front, because it fails ahead of <IP>; the trailing edge is what refuses a
		//                     partial octet ("worker-10.0.0.256" stays as it was). ':' is in no class,
		//                     so a port is never absorbed.
		//
		//     IT RUNS AFTER 4, 4b AND 4c, and that was measured: authorityTail excludes '<', so a HOST
		//     tag behind the '@' made 4 decline on "postgres://u:p@10.0.0.5.nip.io/db" and only the
		//     password was masked. After them it is a plain host behind a credential tag, as 9a sees
		//     one. Before 9a and 9c, so a fused list member reaches 9c as a whole tag and the list
		//     re-anchors on it at no cost to the pass cap.
		//
		//     Its group is raw text only: our own <IP_n> is not its address unit, so the inertness
		//     invariant holds and the mapping cannot nest. The fused value is ONE host -- Restore gives
		//     back "10.0.0.5.nip.io", it recurs under one placeholder, and the address alone elsewhere
		//     is a different value with its own <IP_n>.
		//
		//     WHAT IT COSTS is pinned in TestAcceptedOverMasking: any text glued to an address by '.' or
		//     '-' and carrying a letter is a host now -- a file name (dump-10.0.0.5.log), a missing space
		//     after a sentence dot, a word before the address, a version with a pre-release tag
		//     (1.2.3.4-beta, which 8 already read as an address). Each was <IP_1> beside text in the
		//     clear; now it is one HOST. The other inner tag of this class, an access key as a host
		//     label or with text beside it in a list member, is 9e's since #57.
		//
		//     verify:false, as every host rule: a raw address it ever misses is still 8's on the re-scan.
		{tagHost, mustLongest(ipv4LabelHost), 1, false, false},
		// 8. IPv4 — validated octets, and a leading \b so v1.2.3.4 (a version) is left alone.
		{tagIP, mustLongest(`\b` + ipv4Pattern + `\b`), 0, true, false},
		// 9a. Host inside a URL/URI authority — masked regardless of TLD. The optional
		//     userinfo run skips a (possibly already-masked) credential to reach the host.
		//     Placeholder-tolerant: without it the host stopped at its first <NUM> and
		//     api-gw7.prod.acme.com sent ".prod.acme.com" in the clear, with no second detector
		//     behind it — a public suffix is not on the bare-host allowlist (#43).
		//
		//     The userinfo run admits '@' since #58, for the same reason detector 4's class does:
		//     this is the fourth rule that reads a userinfo, and if it stopped at the first '@'
		//     while the credential rules read to the last, the package would parse one string two
		//     ways. It is the one userinfo run that admits '<' and '>' on purpose -- it steps over
		//     the tag the credential rules minted -- and that is unchanged. Where it showed was only
		//     the shapes every credential rule declined, which were #57's: there the HOST tag
		//     landed on the real host and what leaked was the partly-recognised half's remainder.
		//     4d takes that userinfo whole now, before a key tag can exist inside it.
		//     THE GROUP IS ONE HOST, and since #61 that is a statement about this rule rather than
		//     about the value. A multi-host authority -- host[:port](,host[:port])*, which MongoDB,
		//     MySQL, libpq and Sentinel all write -- is masked by this rule up to the first port colon
		//     or comma and by 9c from there; see 9c for why the rest of the list is a second rule and
		//     not a wider class here.
		//     THE SCHEME IS schemeAnchor, shared with 9c, and since #52 it reads a scheme the pipeline
		//     damaged (s<NUM>://) and one glued to '_' or a digit (x_https://, 2https://). Until then
		//     both rules carried their own copy of \b[a-z][a-z0-9+.-]*://, and #43 widened the host
		//     group beside it without touching it -- see schemeAnchor.
		{tagHost, mustLongest(
			`(?i)` + schemeAnchor + `(?:[^/\s]*@)?(` + hostRun + `)`), 1, false, false},
		// 9c. Hosts 2..n of a comma-separated authority (#61). Runs after 9a, whose tag on the first
		//     member is its context, and BEFORE 9b and 6, both measured: after 9b a later member on a
		//     private suffix carried 9b's own tag and blocked the group ("h1.acme.com,h2.corp.internal,
		//     h3.acme.com" sent h3.acme.com); after 6 a UUID label did the same one member in. The
		//     audit table's numbering is kept -- 9c sits between 9a and 9b, and the chain indices of 9b,
		//     6 and 10 shift by one; nothing hardcodes them.
		//
		//     THE CAUSE IS A FIFTH KIND, and the first instance of it. 9a matched exactly what it was
		//     written for -- one host -- and stopped where its author thought the value ended, at the
		//     port colon and the comma. Not an under-matching pattern (#41), not a value another
		//     component rewrote first (#43), not a detector blocked by another's tag (#46, #48, #54,
		//     #57), not a shape no rule described (#55): the detector's notion of the value was smaller
		//     than the value. residue() could not see it (both host rules are verify:false, and would
		//     have read the leftover as clean regardless, for #41's structural reason) and neither could
		//     the completeness rows, which ask whether the group landed on the DECLARED secret -- it did.
		//     mongodb://user:pass@host1:27017,host2:27017,host3:27017/db sent host2 and host3, in the
		//     trigger line and in the masked template of the same request. v0.4.0 -> v0.9.3.
		//
		//     A COMPANION RULE, NOT A WIDER CLASS ON 9a, and that was measured rather than preferred.
		//     Widening 9a's own group to span the list -- a negated class bounded at '/', whitespace and
		//     '>', or the exact grammar with the trailing port outside, which is what net/url calls the
		//     Host of that URI -- closes every list shape and OPENS a leak on one masked today:
		//     "peers=https://a.com,https://b.com" is <HOST_1>,https://<HOST_2> and would become
		//     <HOST_1>://b.com. A comma member that is the next URL's scheme is a legal reg-name (RFC
		//     3986 §3.2.2 puts ',' in sub-delims, and net/url reads that string as host "a.com,https",
		//     path "//b.com"), and RE2 has no lookahead to refuse it. A trailing context that rejects
		//     "://" cannot go on 9a: a REQUIRED terminator on a first-pass rule narrows what it already
		//     masks, since every position the terminator does not name becomes a no-match. It CAN go on
		//     a companion, because a companion's no-match is today's behaviour -- and that is the durable
		//     result here: disambiguating trailing context is affordable only on a rule whose failure is
		//     the status quo. (Two capture groups on 9a would need `group int` to become a list, and
		//     relaxing 9a's anchor to a bare "://" would have closed #52 by dropping the anchor rather
		//     than making it tolerant; neither was done, and #52 was closed by making it tolerant --
		//     see schemeAnchor.)
		//
		//     ONE MEMBER PER MATCH, RE-APPLIED UNTIL NOTHING CHANGES (#67). Until #67 the group was hosts
		//     2..n as one run, and that run excludes '<' -- the inertness invariant -- so a tag minted on a
		//     LATER member before this rule ran ended it there, and every member after the tag was sent:
		//     mongodb://host1:27017,10.0.0.5:27017,host3:27017/db sent host3. 7 and 8 reach a later member
		//     (an address literal, bracketed or not) and are the ordinary way in; 1, 2 and 3b only when a
		//     member literally IS a key. Now the group is ONE member and the context steps over every
		//     member already tagged, so each pass takes the first raw member after a run of tags -- and
		//     the tags it steps over are its own from the pass before as readily as 7's or 8's. Mask
		//     re-applies the rule until a pass changes nothing (applyToFixpoint, which states why that
		//     terminates and what happens at its cap). This is the same containment #54 names, but the
		//     inner tag is RIGHT here -- an address in a list is an address -- so the remedy is to
		//     re-anchor on it rather than to undo it, and #54's pre-pass is not needed for it.
		//
		//     THE SHAPE OF THE RULE, part by part.
		//       anchor and userinfo skip: 9a's -- schemeAnchor, one definition since #52 -- so the two
		//                                 rules read one string one way.
		//       member 1, context:        a raw host run or a taggedMember. Derived by walking the chain:
		//                                 before 9c runs a tag can land on member 1 from 7 and 8 (an
		//                                 address literal), from 9a, from 1/2/3b only when a host literally
		//                                 IS a key, from 9d and 9e (an address or a key fused with labels),
		//                                 and two tags adjacent when a key is directly followed by an
		//                                 address literal. 5 reached it only through #57's shape until 4d
		//                                 took that userinfo. 3a needs whitespace, 4 mints on the
		//                                 userinfo, 9b/6/10 run later. The raw branch is unreachable in the chain -- 9a has
		//                                 already taken every raw first member this anchor can reach, and
		//                                 declines only on '<' and '[', where the raw branch cannot start
		//                                 either -- and exists for the single-detector probes
		//                                 completenessSecretFor and the templatized-coverage test run,
		//                                 exactly as 4b/4c's context does.
		//       its port, one or more commas: libpq accepts an empty list item, so ",," is a separator.
		//       members 2..k-1, context:  taggedMember only, each with its port and its commas. NO raw
		//                                 member is admitted here, and that is what makes the parse unique:
		//                                 were one admitted, leftmost-longest would take the LAST raw member
		//                                 as the group. A bare pipeline <IP> on the Template field is not
		//                                 admitted either; it is a host run, masked as a member, exactly
		//                                 as 9a masks https://<IP>/x.
		//       member k, THE GROUP:      one host run, its port outside -- the boundary 9a draws for one
		//                                 host. The class is 9a's; '?', '#', '/', '@', ',', whitespace, '<'
		//                                 and '>' are out by construction, which is why the '?'/'#'
		//                                 question #58 answered for a NEGATED userinfo class does not arise
		//                                 here. Measured, not analogised.
		//       listEnd:                  the group ends at end of text, at any character outside the
		//                                 host class -- the next comma included -- or at a ':' that does not
		//                                 begin "://". Excluding '<' from the terminator stops the run
		//                                 shrinking onto a pipeline placeholder ("host<NUM>://x" masked
		//                                 "host" until it did); the group cannot shrink onto a host
		//                                 character either, which is what rules out a partial member. Case
		//                                 is folded under (?i), negated class included -- measured, since an
		//                                 uppercase terminator would have truncated a member with no test
		//                                 looking for it.
		//
		//     WHAT IT COSTS is pinned in TestAcceptedOverMasking: a path-less URL directly followed by a
		//     comma and CSV cells reads as a host list -- a cost to the payload the model reasons about
		//     rather than to a lossy restatement, named there as a new kind -- and since #67 each cell is
		//     its own placeholder, so equal cells share one. Past maxHostListPasses members the
		//     escalation is skipped rather than sent (see applyToFixpoint). The cost #61 named first, the
		//     ports between members hidden inside one placeholder, is gone: every port is visible now,
		//     and a member that recurs elsewhere in the request gets the same placeholder there.
		//
		//     WHAT IT DOES NOT REACH is a MIXED member, a tag with raw text glued to it, which is neither a
		//     tag nor a host run, so the list ends there. Two rules once produced one, and each now runs
		//     first and hands this rule the whole name as one tag: 9d for #54's address inside a hostname,
		//     and 9e for #57's key with text beside it (AKIA...EXAMPLE.prod, <TOKEN_1>.prod on main).
		//
		//     verify:false, for the same reason 9a and 9b are: a missed list member must not mute an
		//     otherwise-safe escalation.
		{tagHost, mustLongest(
			`(?i)` + schemeAnchor + `(?:[^/\s]*@)?` +
				`(?:` + hostRun + `|` + taggedMember + `)(?:` + hostPort + `)?,+` +
				`(?:` + taggedMember + `(?:` + hostPort + `)?,+)*` +
				`(` + hostRun + `)(?:` + hostPort + `)?` + listEnd), 1, false, true},
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
		//
		//     A CONFIGURED SUFFIX IS TOLERANT TOO since #105, and the built-ins are byte-identical:
		//     none of them is one the pipeline rewrites. See bareHostPattern.
		{tagHost, bareHost, 0, false, false},
		// 6. UUID — a fixed 8-4-4-4-12 shape. It runs HERE, after both host detectors, and the
		//    out-of-sequence number is deliberate: the audit table, the README and the issues all
		//    say "detector 6", "9a", "9b", so renumbering the chain to match position would
		//    invalidate every one of them.
		//
		//    It ran before them until #48, and that was a leak. A UUID used as a hostname LABEL
		//    minted a <UUID_n> inside the host detectors' span; every host class excludes '<' and
		//    '>', so the host was masked in part or not at all —
		//    https://<uuid>.blob.core.windows.net sent the whole domain with nothing behind it,
		//    api.<uuid>.acme.com sent ".acme.com", worker-<uuid>.corp.internal sent "worker-".
		//    Both host detectors are verify:false, so residue() reported clean and the escalation
		//    was sent.
		//
		//    WHY MOVING THIS ONE DOES NOT BREAK MOST-SPECIFIC-FIRST is argued in full in the
		//    package comment, and the short form is that 6 and the host detectors are not in a
		//    specificity relation at all: a UUID standing alone is unreachable by both, so they
		//    never compete, and the only text they contend for is a UUID INSIDE a host — where
		//    the host is the right answer. Containment, not competition.
		//
		//    An IP is the opposite case and stays where it is; the address inside a name is 9d's, a
		//    rule rather than a move. See the package comment.
		//
		//    Placement is between 9b and 10 rather than at the end: keeping it ahead of the
		//    home-dir detector preserves /home/<uuid>/app.log as <UUID_1> rather than <USER_1>.
		{tagUUID, mustLongest(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), 0, true, false},
		// 10. Home directory — mask only the username segment, keeping the rest of the path
		//     so /home/<USER_1>/go/pkg/mod/... stays useful in a stack trace. Tolerant, and the
		//     awkward position is the one worth naming: a TRAILING digit was always harmless
		//     (/home/maxie7 → /home/<USER_1><NUM> loses nothing), while a digit in the MIDDLE
		//     split the value and sent the tail — /home/deploy2prod leaked "prod" (#43).
		//
		//     THE NAME ENDS BEFORE A TRAILING RUN OF CLOSERS (#90). The class stopped at nothing a log
		//     writes AFTER a value, so a name at the end of a quoted or bracketed one took the closer with
		//     it: msg="cannot read /home/bob" lost its quote into <USER_1>, {"path":"/home/bob"} its "}.
		//     Nothing was disclosed, but the model was sent a broken payload and an answer naming <USER_1>
		//     restored with the stray characters. The body keeps the class, so a closer INSIDE a name
		//     (/home/o'brien/x) is still masked; only the last unit may not be one (see homeUser). What it
		//     costs, pinned: a name whose last character is a closer loses it before a '/' too, and a
		//     "name" made only of closers (/home/../etc) is none. Only closers leave a placeholder.
		{tagUser, mustLongest(`(?:/home/|/Users/)(` + homeUser + `)`), 1, true, false},
	}
	// 11. A key glued to a word character (#108), one rule per key, in 1, 2 and 3b's order. Their \b
	//     fails beside '_' or a letter, so until #108 such a key was no key at all on the raw line:
	//     "key AKIA...EXAMPLE_prod used" was sent with the key in it and residue() read it as clean. On
	//     the template the mangled form has no closing \b, so there the key was masked and "_prod" sent.
	//
	//     IT RUNS LAST, and that is the whole design. Teaching 1, 2 and 3b the new edges was rejected:
	//     main masks a glued key WHOLE wherever a rule owns the value around it -- a bearer token (3a), an
	//     email local part (5), a bare private-suffix host (9b), a userinfo (4), a host (9a, 9c) -- and it
	//     does so only because the key rules do NOT see the key. A key tagged first blocks every one of
	//     them, the inertness invariant's wall, and the rest of the value is sent: "Bearer AKIA..._prod"
	//     would send "_prod", "x_AKIA...@example.com" the domain, "worker_AKIA....corp.internal" "worker".
	//     That is the anti-correlation BACKLOG records for #51, a key rule that sees more blocking a rule
	//     after it. Run after every rule, this one reads only text nothing claimed, so it can only add a
	//     placeholder: every byte masked before is masked the same way, in the same layout.
	//
	//     There the text beside a key is PROSE, #57's decision: the key is the value and the glue stays,
	//     "key <TOKEN_1>_prod used", "key x_<TOKEN_1> used". The character before the key is context, not
	//     group, which a key rule could not do: RE2 has no lookbehind, and 1, 2 and 3b mask group 0. Since
	//     #51 '_' and '-' are an sk key's own alphabet, so for an sk key only the glue BEFORE it is prose.
	//
	//     verify:true, as every key rule: a raw glued key that ever survives masking mutes the escalation.
	//     Its group is a raw key only, so it cannot fire on our own output.
	for _, k := range keyShapes {
		dets = append(dets, detector{tagToken, mustLongest(looseKey(k.loose, k.lead)), 1, true, false})
	}
	// 12. A vendor key (#114): Stripe, GitHub classic and fine-grained, Slack and Google, one rule per
	//     format family in vendorKeys' order. None was any detector's shape, so each was sent whole, raw
	//     and on the template, and residue() read the line as clean.
	//
	//     IT RUNS LAST, after rule 11, and the argument is #108's. A vendor key no strict detector tags is
	//     masked whole by every rule that owns a value around it -- a userinfo half (4), a host or a list
	//     member (9a, 9c), a bare private host (9b), an email local part (5), a home directory (10), a
	//     bearer value (3a) -- because their classes admit the key's alphabet. A key rule run at 3b's slot
	//     would tag it first and block each of them at our '<', the anti-correlation #51 paid twice: the
	//     domain of "mail <key>@example.com" and the labels before a bare host's key label would be sent.
	//     Run last, it reads only text no rule claimed, so every rule before it sees main's input and only
	//     placeholders are added. After rule 11 rather than before, so main's whole chain is a prefix.
	//
	//     There the text beside a key is prose, #57's decision: glue BEFORE the prefix stays outside
	//     ("token_<TOKEN_1>"), and after it the format's alphabet decides, so a '_' after a github_pat_ or
	//     Google key, and a '-' after a Slack or Google one, are the key's, while a closer never is.
	//
	//     One rule per format family rather than one for all, so each family has its own canonical
	//     completeness and templatized rows; GitHub is two, because its formats differ in alphabet and floor.
	//
	//     verify:true, as every key rule: a raw vendor key that ever survives masking mutes the escalation.
	//     Its group is raw text and the pipeline's placeholders only, so it cannot fire on our own output.
	for _, k := range vendorKeys {
		dets = append(dets, detector{tagToken, mustLongest(looseKey(k.loose, k.lead)), 1, true, false})
	}
	return dets
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
// #52 ADDED THE OTHER HALF: the rule covers a detector's ANCHORS, not only its group. 9a's scheme
// was context, so the group checks above it were green while s3:// templatized to s<NUM>:// and the
// host was sent; TestEveryDetectorsContextSurvivesTemplating asks the question of every context.
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
// It appears in three rules -- the two one-sided credential fallbacks, 4b and 4c, and the host-list
// rule 9c -- and in each only as CONTEXT, never inside a target group. That distinction is the whole of the
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

// ourPHRe is ourPH compiled once, for Restore. A tag that does not match [A-Z]+ would mint a
// token Restore never finds; TestEveryTagMintsARestorableToken reads the tags from the detector
// tables so such a tag cannot be added unnoticed.
var ourPHRe = regexp.MustCompile(ourPH)

// schemeAnchor is the URL scheme as 9a and 9c find it: a letter, then scheme characters or pipeline
// placeholders, then "://". It is CONTEXT for both rules, never group, and it is ONE definition
// because two copies of it were how it was missed (#52). TestSchemeAnchorIsShared holds both rules
// to it.
//
// THE RUN IS TOLERANT. #43 made every detector's GROUP read the pipeline's placeholders and left
// this anchor alone, so a scheme with a digit -- s3, h2 -- templatized to s<NUM>:// and both rules
// went quiet on the Template field: url=s<NUM>://<TOKEN_1>:<TOKEN_2>@bucket sent "bucket". A
// detector's tolerance has to cover its anchors, not only its group, and
// TestEveryDetectorsContextSurvivesTemplating now checks that for every detector's context. The
// leading letter needs none: a scheme begins with one, and the pipeline never replaces a lone letter.
//
// THERE IS NO LEADING ASSERTION, and that was argued rather than dropped. Leftmost-longest already
// starts a scheme at the earliest letter whose run reaches "://", so a letter before a scheme is part
// of it -- "xhttps" is a scheme, as it always was. What the old \b refused was only a start after a
// '_' or after a digit no earlier letter reaches, and both were leaks: x_https://acme.com/x and
// __https://acme.com__ were sent whole, and 2https://acme.com/x was sent on the raw line while its
// template, <NUM>https://, was masked. With no assertion the anchor is (?:^|[^a-z]) in effect, and
// nothing that may legally precede a scheme is refused. "foo_bar://" reads as scheme "bar"; it is
// not a legal scheme either way, and masking the host behind it is the safe reading.
//
// What it does not reach is a scheme inside one of OUR tags: "Bearer https://acme.com/x" gives 3a
// the scheme as a token, "<TOKEN_1>://acme.com/x", and no letter is left. That is interference, not
// templating, and it is a cell of the authority audit (#65).
const schemeAnchor = `[a-z](?:[a-z0-9+.-]|` + pipelinePH + `)*://`

// hostRun is one host as 9a and 9c read it: a run of hostname characters, placeholder-tolerant
// for the Template field. One definition, so that "a host" cannot drift between the two rules.
//
// '_' is in the class since #66. RFC 3986 puts it in unreserved, so it is legal anywhere in a
// reg-name, and without it http://my_host:8080/x sent "_host:8080" and a dotted name sent its
// whole domain behind the '_'. A flat run, so '_' is admitted in any position, a trailing one
// included -- that over-mask is pinned in TestAcceptedOverMasking.
var hostRun = tolerant(`[a-z0-9._-]`)

// ipv4Octet is one validated octet and ipv4Pattern an address of four: detector 8's, shared with 9d
// so that "an address" cannot drift between the rule that masks one alone and the rule that masks
// one inside a name.
const (
	ipv4Octet   = `(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])`
	ipv4Pattern = `(?:` + ipv4Octet + `\.){3}` + ipv4Octet
)

// ipv4LabelHost is 9d: an address joined to hostname labels across '.' or '-', the group, between
// two single characters of context. See 9d for each part.
//
// The group is two alternatives because "at least one side carries a letter" is not one regular
// shape: the left side carries it and the right is optional, or the left is optional and the right
// carries it. A side is a run that starts and ends on a label character; a side "with a letter" is
// the same run with a literal [a-z] somewhere in it, which a pipeline placeholder's own letters
// cannot supply, '<' being in no class.
var ipv4LabelHost = func() string {
	char := `(?:[a-z0-9_]|` + pipelinePH + `)`  // starts and ends a side
	run := `(?:[a-z0-9_.-]|` + pipelinePH + `)` // anything inside one
	addr := `(?:` + ipv4Pattern + `|<IP>)`
	left := char + `(?:` + run + `*` + char + `)?[.-]`
	right := `[.-]` + char + `(?:` + run + `*` + char + `)?`
	leftL := `(?:` + char + run + `*)?[a-z](?:` + run + `*` + char + `)?[.-]`
	rightL := `[.-](?:` + char + run + `*)?[a-z](?:` + run + `*` + char + `)?`
	return `(?i)(?:^|[^a-z0-9_])(` +
		leftL + addr + `(?:` + right + `)?` + `|` +
		`(?:` + left + `)?` + addr + rightL +
		`)(?:[^a-z0-9_]|$)`
}()

// hostPort is the ":port" that may follow a host, tolerant because a port templatizes to <NUM>.
var hostPort = `:` + tolerant(`[0-9]`)

// taggedMember is a host-list member some rule has already masked, as 9c reads it for context:
// one or more adjacent tags of ours, or a bracketed tag (ours, or the pipeline's <IP> on the
// Template field). Member 1 and every member before 9c's group share it, so the two cannot drift.
const taggedMember = `(?:(?:` + ourPH + `)+|\[(?:` + ourPH + `|` + pipelinePH + `)\])`

// listEnd is what may follow the last member of a host list in 9c: end of text, any character
// outside the host class, or a ':' that does not begin "://". It is CONTEXT, never group, and it
// exists to refuse exactly one thing -- a comma member that is the next URL's scheme -- which a
// group alone cannot refuse, RE2 having no lookahead. '<' is excluded so the run cannot shrink onto
// a pipeline placeholder to satisfy the terminator. See 9c.
//
// '_' STAYS A TERMINATOR although hostRun admits it since #66, and that was measured, not missed.
// The group can stop at a terminator only where the full member fails, which is a member in scheme
// position: "mongodb://h1,h2_x://y". With '_' terminating, the group falls back to "h2" and masks
// it, exactly as before #66. Were '_' a host character here, no shorter member could end legally,
// 9c would decline, and "h2" would be sent: a widening that narrows.
const listEnd = `(?:[^a-z0-9.:<-]|:(?:[^/]|/[^/]|$)|$)`

// authorityTail is what follows the DELIMITING '@' of a userinfo: host[:port], then the end of
// the authority. It is CONTEXT, never group, and it contains no '@' by construction -- which is
// the whole of RFC 3986 §3.2's disambiguation of a raw '@' inside a userinfo, and the reason
// detector 4 can admit '@' into its class without matching a prefix (#58).
//
// The host run is placeholder-tolerant for the Template field (db<NUM>.corp.internal) and
// otherwise negated, so a port, a bracketed IPv6 literal, a comma-separated host list and a
// query with no '@' in it all read as "the rest of the authority" and cost nothing. The
// terminator is '/', whitespace, '>' or the end of the text. '>' is there for an
// angle-bracketed URL, "<postgres://u:p@db>". '<' is deliberately NOT a terminator: detector 4
// runs before every host detector, so a '<' after the host at that point can only be a tag
// minted by 1-3b -- and 1-3b mint tags inside the USERINFO, which means the '@' just crossed was
// a raw one and the match must fail so that 4b or 4c can take the half.
const authorityTail = `(?:[^@/\s<>]|` + pipelinePH + `)*(?:[/\s>]|$)`

// jwtChar is the base64url alphabet of a JWT segment.
const jwtChar = `[A-Za-z0-9_-]`

// The parts of a key: its prefix and the alphabet of the run after it, or for a JWT its three segments.
// Each key below is built from them twice, with two different edges, so the two cannot drift.
const (
	awsPrefix = `(?:AKIA|ASIA)`
	awsChar   = `[0-9A-Z]`
	skPrefix  = `sk-`
	skChar    = `[A-Za-z0-9]`
	skChars   = `A-Za-z0-9_-`
	skRun     = `[` + skChars + `]`
)

var jwtBody = `eyJ` + tolerant(jwtChar) + `\.eyJ` + tolerant(jwtChar) + `\.` + tolerant(jwtChar)

// skBody is what follows "sk-" in a key (#51). The tail is a run of skRun -- '-' and '_' are key
// characters, as in every current format (sk-proj-, sk-svcacct-, sk-admin-, sk-ant-api03-) -- and a run
// of that alphabet is a key only when it carries one of two witnesses, because kebab-case names that
// start with a literal "sk-" word (sk-learn-compatible-estimator, sk-worker-pool-7f9c) are the same
// alphabet:
//
//   - sixteen unbroken letters and digits anywhere in it: the classic key's own run, which until #51 had
//     to start right after "sk-", allowed to come after a vendor prefix (sk-or-v1-<hex>);
//   - an uppercase letter, with the whole tail sixteen long: a base62 or base64url body. Names on k8s,
//     Docker, PyPI and npm are lowercase by rule. The floor is over the whole tail, not one run.
//
// On the Template field the pipeline breaks both witnesses' runs, and the placeholder is the evidence, as
// for mangled: main's mangled form, then the rest of the run across separators -- so the key still ends
// where its alphabet does -- or a run holding a placeholder and an uppercase letter. A placeholder alone
// is not enough once '-' is admitted: sk-worker-pool-7f9c templatizes to sk-worker-pool<NUM>f<NUM>c.
//
// No alternative carries a trailing assertion. Each is maximal over skRun (with the pipeline's
// placeholders on the template), so leftmost-longest ends a key where its alphabet ends, which is rule
// 11's notion since #108; and that is what makes skRun the sk key's cont in keyShapes, for 4d and 9e.
var skBody = func() string {
	unit := `(?:` + skRun + `|` + pipelinePH + `)`
	raw := skRun + `*` + skChar + `{16}` + skRun + `*|` + withUpper(16)
	tmpl := mangled(skChar) + unit + `*|` +
		unit + `*(?:` + pipelinePH + unit + `*[A-Z]|[A-Z]` + unit + `*` + pipelinePH + `)` + unit + `*`
	return `(?:` + raw + `|` + tmpl + `)`
}()

// skDigitFirst is what follows "sk<NUM>": a classic key whose tail starts with a digit, on the Template
// field, where NUM takes the sign and leaves no "sk-" (sk-7AbC... templatizes to sk<NUM>AbC...). Until
// #51 the rest of the key was sent there.
//
// It carries a WITNESS, not a floor. The placeholder stands for an unknown count of digits, so any floor
// is a guess, and an exact one would be withUpper over the placeholder-tolerant unit. And the placeholder
// is weaker evidence here than in mangled, since it is also every "sk-1": so the witness is stronger than
// the raw tail's, a letter of each case -- a base62 body carries both, while "sk-1 failed", "region sk-2"
// and a size like "sk-2XL" carry at most one -- or sixteen unbroken letters and digits. A digit-first hex
// body needs neither: HEX runs before NUM and keeps the "sk-" (sk-<HEX>).
var skDigitFirst = caseOrRun(`(?:`+skRun+`|`+pipelinePH+`)`, skChar)

// caseOrRun is a run of unit carrying a letter of each case or sixteen unbroken characters of char:
// skDigitFirst's witness after a placeholder that stands for an unknown count of digits, and Slack's
// after its own (see slackKey).
func caseOrRun(unit, char string) string {
	return unit + `*(?:[A-Z]` + unit + `*[a-z]|[a-z]` + unit + `*[A-Z]|` + char + `{16})` + unit + `*`
}

// skLoose is the sk key with no edge of its own, rule 11's; skKey adds the leading \b.
var skLoose = `(?:` + skPrefix + skBody + `|sk<NUM>` + skDigitFirst + `)`

// withUpper is a run of at least n characters of skRun, one of them an uppercase letter. RE2 has no
// intersection, so it is n branches, one per position the FIRST uppercase letter can take: before it
// only non-uppercase characters, after it whatever the floor still needs.
func withUpper(n int) string {
	const lower, upper = `[a-z0-9_-]`, `[A-Z]`
	alts := make([]string, 0, n)
	for k := 0; k < n-1; k++ {
		before := ""
		if k > 0 {
			before = fmt.Sprintf(`%s{%d}`, lower, k)
		}
		alts = append(alts, fmt.Sprintf(`%s%s%s{%d,}`, before, upper, skRun, n-1-k))
	}
	alts = append(alts, fmt.Sprintf(`%s{%d,}%s%s*`, lower, n-1, upper, skRun))
	return `(?:` + strings.Join(alts, "|") + `)`
}

// jwtKey, awsKey and skKey are what detectors 1, 2 and 3b match: "a key", in one definition. 4d and
// 9e read the same three to find a key inside a larger value, so the rule that would tag a key and the
// rule that must get to it first cannot disagree about what one is (TestGluedKeyRulesReadTheKeyDetectors).
//
// These are the STRICT keys: a \b on the leading edge, and on the trailing one for JWT and AWS, so a key
// standing alone in its value. That is the notion 4d and 9e rely on -- a key that stands alone keeps its
// own tag -- and it is why a key glued to a word character is not one of these (#108; see looseKey). An
// sk key's trailing edge is its alphabet since #51 (see skBody): '_' is in it, so no word character can
// follow one.
var (
	jwtKey = `\b` + jwtBody
	awsKey = `\b` + awsPrefix + `(?:` + awsChar + `{16}\b|` + mangled(awsChar) + `)`
	skKey  = `\b` + skLoose
)

// keyShapes pairs each key with the raw characters it could have continued with, as a class body.
// A JWT's third segment runs on through '-' and '_', so neither is text beside a JWT.
//
// loose and lead are rule 11's (#108). loose is the same key with no edge of its own: its run ends where
// its alphabet does, which leftmost-longest gives without an assertion, and an AWS run longer than
// sixteen is a key too, as its mangled form always was. lead is what may NOT sit before a loose key:
// the key's own letters and digits, which would make a longer word that merely contains the prefix
// (XAKIA..., task-..., xeyJ...). '_' and '-' are separators, so a JWT's lead is its alphabet without
// them, and an AWS key's, being uppercase, leaves a lowercase letter as glue. An sk key's lead stays its
// letters and digits although '_' and '-' are in its alphabet since #51: before "sk-" they are still
// separators, so x_sk-... is a key glued to '_'.
var keyShapes = []keyShape{
	{jwtKey, `A-Za-z0-9_-`, jwtBody, `A-Za-z0-9`},
	{awsKey, `0-9A-Z`, awsPrefix + `(?:` + awsChar + `{16,}|` + mangled(awsChar) + `)`, `0-9A-Z`},
	{skKey, skChars, skLoose, `A-Za-z0-9`},
}

// keyShape is one key as keyShapes lists it.
type keyShape struct{ pat, cont, loose, lead string }

// vendorKeys are rule 12's keys (#114), one per format family: a vendor's prefix, then a body over the
// format's own alphabet with no edge of its own, so a key ends where its alphabet does, as rule 11's do.
// lead is what may not sit before the prefix, as for rule 11: a letter or a digit there makes a longer
// word that merely contains the prefix (disk_test_..., bAIza...), and '_', '-' and punctuation are glue.
//
// They are NOT keyShapes, and that is the point of the list. keyShapes exists so that 4d, 9e and 3c can
// see a key inside a larger value before a strict detector tags it there; no strict detector tags a vendor
// key, so the rules that own a value around one -- 4, 9a, 9c, 9b, 5, 10, 3a -- already mask it whole,
// and only text none of them claims is left for rule 12.
var vendorKeys = []vendorKey{
	{stripeKey, `A-Za-z0-9`},
	{githubKey, `A-Za-z0-9`},
	{githubPAT, `A-Za-z0-9`},
	{slackKey, `A-Za-z0-9`},
	{googleKey, `A-Za-z0-9`},
}

// vendorKey is one key as vendorKeys lists it.
type vendorKey struct{ loose, lead string }

// The vendor formats, each from its published structure. A raw body's floor is the shortest published
// length, with no upper bound: a vendor that lengthens its keys still matches, and a longer run of the
// alphabet is masked whole, as rule 11's AWS run is. On the Template field the pipeline turns each digit
// run into <NUM> and a placeholder is the evidence, as in mangled, with a floor in units (mangledFloor),
// so a short identifier with a digit in it stays.
//
//   - Stripe: sk_ and rk_ (secret and restricted) in live or test mode, 24 base62 characters for a test or
//     legacy key and up to about 99 for a current one; whsec_ (a webhook signing secret) with 32, or 64
//     hex from the Stripe CLI. pk_ is a publishable key, public by design, and is not read.
//   - GitHub classic: ghp_, gho_, ghu_, ghs_, ghr_ and 36 base62 characters, the last six a checksum.
//   - GitHub fine-grained: github_pat_ and 82 characters, '_' among them (22, '_', 59). The split is not
//     relied on; the floor is over the alphabet.
//   - Slack: xoxb-, xoxp-, xoxa-, xoxr-, xoxs- and xapp-, a body of '-'-separated segments that starts
//     with a numeric one and carries a secret segment of at least sixteen (24 base62 for a bot, 32 hex
//     for a user, 64 hex for an app token; the IDs are at most thirteen digits). '-' is in the alphabet,
//     so a kebab-case word after a literal "xoxb-" is the same alphabet, and the witness is #51's run.
//   - Google: AIza and exactly 35 base64url characters.
//
// The template floors are measured, not guessed: over 200,000 simulated bodies per format the fewest
// units left on the Template field were 13 for a hex whsec_, 14 for a 24-character sk_, 27 for ghp_, 69
// for github_pat_ and 25 for AIza, and every one of those templates matched whole. Each floor sits far
// enough below its minimum that mangledFloor's two branches meet it (see there), and far enough above
// a short identifier with a digit in it ("ghp_v2", "AIza_test_1") that one stays.
var (
	stripeKey = `(?:(?:sk|rk)_(?:live|test)_(?:` + vendorAlnum + `{24,}|` + mangledFloor(vendorAlnum, 8) + `)` +
		`|whsec_(?:` + vendorAlnum + `{32,}|` + mangledFloor(vendorAlnum, 8) + `))`
	githubKey = `gh[pousr]_(?:` + vendorAlnum + `{36,}|` + mangledFloor(vendorAlnum, 12) + `)`
	githubPAT = `github_pat_(?:[A-Za-z0-9_]{82,}|` + mangledFloor(`[A-Za-z0-9_]`, 24) + `)`
	googleKey = `AIza(?:[A-Za-z0-9_-]{35,}|` + mangledFloor(`[A-Za-z0-9_-]`, 12) + `)`
)

// vendorAlnum is the base62 alphabet of the Stripe and GitHub classic bodies.
const vendorAlnum = `[A-Za-z0-9]`

// slackKey is the Slack family. On the raw line the body starts with a digit and carries an unbroken run
// of sixteen; a team or user ID is never that long, and a kebab-case word after "xoxb-" has neither.
//
// On the Template field the leading '-' and the first numeric segment are one <NUM> -- NUM takes a '-'
// before a digit as its sign -- so "xoxb-123...-456...-AbC..." arrives as "xoxb<NUM><NUM>-AbC...", and the
// witness is caseOrRun's, as after sk<NUM>: the placeholder stands for any count of digits, and it is also
// every "xoxb-1". A user or app token's hex segment arrives as <HEX>, with no secret byte left in the
// template, so that template is main's.
var slackKey = `(?:xox[abprs]|xapp)(?:-[0-9][A-Za-z0-9-]*` + vendorAlnum + `{16}[A-Za-z0-9-]*|<NUM>` +
	caseOrRun(`(?:[A-Za-z0-9-]|`+pipelinePH+`)`, vendorAlnum) + `)`

// mangledFloor is mangled with a floor of n units, a unit being a character of class or a whole pipeline
// placeholder: a run with a placeholder in it and at least n-1 units on one side of its FIRST placeholder.
// RE2 has no intersection, so "at least n units, one of them a placeholder" exactly would be n branches,
// as withUpper is; this is two, and loses nothing a key needs. It never matches fewer than n units, and it
// matches every such run of 2n-2 units or more: with the first placeholder at unit p of U, the first
// branch fails only for p < n-1 and the second only for p > U-n, and both fail only if U < 2n-2.
func mangledFloor(class string, n int) string {
	unit := `(?:` + class + `|` + pipelinePH + `)`
	return fmt.Sprintf(`(?:%s{%d,}%s%s*|%s*%s%s{%d,})`, class, n-1, pipelinePH, unit, class, pipelinePH, unit, n-1)
}

// looseKey is rule 11 for one key: the loose key, after the start of the text or a character outside
// its lead. The character before is CONTEXT and the key is the group, so the glue stays where it was.
func looseKey(loose, lead string) string {
	return `(?:^|[^` + lead + `])(` + loose + `)`
}

// glued is a run of unit that contains one of shapes' keys with something beside it: one or more units
// before the key, or directly after it a raw character from after(cont), which must be a character of
// the run that the key could not have continued with.
//
// That exclusion is what makes "beside" mean beside the WHOLE key. On the Template field a key takes
// its mangled form, which carries no closing \b, so AKIAIOSFODNN<NUM> alone is a key to the pattern,
// and without it AKIAIOSFODNN<NUM>EXAMPLE -- a key that is a whole half -- would read as one glued to
// "EXAMPLE". A unit before the key needs no such care: the key starts at its \b, where its detector
// would start too.
func glued(shapes []keyShape, unit string, after func(cont string) string) string {
	right := make([]string, len(shapes))
	keys := make([]string, len(shapes))
	for i, k := range shapes {
		right[i] = k.pat + after(k.cont)
		keys[i] = k.pat
	}
	return `(?:` + unit + `*(?:` + strings.Join(right, "|") + `)` + unit + `*|` +
		unit + `+(?:` + strings.Join(keys, "|") + `)` + unit + `*)`
}

// gluedUserinfo is 4d: detector 4's group, matched only when a half carries a glued key. The username
// runs to the first ':' and the password from there to the delimiting '@', as 4b and 4c read them, so
// a ':' after a key ends a username but is text beside a key in a password.
var gluedUserinfo = func() string {
	user := `(?:[^:/\s<>]|` + pipelinePH + `)`
	pass := `(?:[^/\s<>]|` + pipelinePH + `)`
	inUser := glued(keyShapes, user, func(cont string) string { return `[^:/\s<>` + cont + `]` })
	inPass := glued(keyShapes, pass, func(cont string) string { return `[^/\s<>` + cont + `]` })
	return `://(` + inUser + `(?::` + pass + `*)?|` + user + `*:` + inPass + `)@` + authorityTail
}()

// gluedHost is 9e: a host run with a glued key inside it, after one "://", '@' or ','. The host class
// is a positive one, so the characters a key could not have continued with are listed rather than
// excluded (hostCharsBut).
var gluedHost = func() string {
	const hostChars = `A-Za-z0-9_.-`
	unit := `(?:[` + hostChars + `]|` + pipelinePH + `)`
	return `(?:://|@|,)(` + glued(keyShapes, unit, func(cont string) string { return hostCharsBut(hostChars, cont) }) + `)`
}()

// bearerChars is the alphabet of a bearer value as 3a reads it, RFC 6750's b64token, and bearerChar its
// class. One definition, read by 3a and 3c, so the value 3c takes whole is the value 3a would have.
const (
	bearerChars = `A-Za-z0-9._~+/=-`
	bearerChar  = `[` + bearerChars + `]`
)

// gluedBearer is 3c: a bearer value with a JWT or an AWS key glued inside it, after the word "bearer"
// and whitespace, as 3a finds one. Only the word is case-folded, so a key's case means what it does to
// 1 and 2. It reads keyShapes' first two keys only, 1's and 2's, the key detectors that run before 3a;
// 3b runs after it, so a bearer value with an sk- key inside it has always been 3a's whole.
var gluedBearer = func() string {
	unit := `(?:` + bearerChar + `|` + pipelinePH + `)`
	return `\b(?i:bearer)\s+(` + glued(keyShapes[:2], unit, func(cont string) string { return hostCharsBut(bearerChars, cont) }) + `)`
}()

// hostCharsBut is the class of printable ASCII characters in class and not in cont, each written as
// an escape so that '-', '.' and ']' mean themselves. 9e reads it over the host class, 3c over the
// bearer class.
func hostCharsBut(class, cont string) string {
	in, out := regexp.MustCompile(`^[`+class+`]$`), regexp.MustCompile(`^[`+cont+`]$`)
	var b strings.Builder
	b.WriteString("[")
	for c := '!'; c <= '~'; c++ {
		if in.MatchString(string(c)) && !out.MatchString(string(c)) {
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	b.WriteString("]")
	return b.String()
}

// homeChar is a home-dir username character as 10 reads it, and homeEnd one that may END the name: the same
// class without the trailing set, the closers a log writes after a value -- quotes and the backtick, closing
// brackets, a comma, a semicolon, a sentence dot, and the backslash that escapes a closing quote inside a
// quoted value ("open \"/home/bob\""). '!', '?', '-', '_' and '$' stay name characters: no formatter writes
// the first two after a value, and the last three are legal in a username ('$' ends a Samba machine account).
const (
	homeChar = `[^/<>\s:]`
	homeEnd  = `[^/<>\s:"'` + "`" + `)\]},;.\\]`
)

// homeUser is 10's group: a run of homeChar that ends on a homeEnd, either of them a pipeline placeholder
// too, so the name stays tolerant at its end (#43). Leftmost-longest makes it the longest such run, so a
// closer inside the name is kept and a trailing run of them is left outside, however long.
var homeUser = `(?:` + homeChar + `|` + pipelinePH + `)*(?:` + homeEnd + `|` + pipelinePH + `)`

// tolerant builds a run of one or more units, where a unit is a character from class OR a
// whole pipeline placeholder. For detectors with no length requirement, that is the entire
// fix: the run simply steps over the pipeline's damage.
func tolerant(class string) string {
	return `(?:` + class + `|` + pipelinePH + `)+`
}

// mangled builds a run that contains AT LEAST ONE pipeline placeholder.
//
// It exists for the two detectors carrying a length requirement — sixteen unbroken letters and
// digits in an sk- tail, exactly {16} after AKIA/ASIA — which a placeholder breaks by collapsing a
// whole digit run into one token. (The sk tail extends it over its separators; see skBody.) Rather than lower those thresholds (which would widen false positives on ordinary
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
//
// The pipeline's template mask has its own, ipv6Pat in internal/pipeline/template.go, and the two
// stay two on purpose (#42). That one wants a boundary before the match and two hex groups,
// because there an over-match corrupts the card (#33). Here an under-match is disclosure. So this
// one keeps what that one drops: "::1", "fe80::/64", and an address glued onto the token before it.
// It pays for that in the prompt: a C++ scope operator is redacted as an address
// (CDBusNMHelper<IP_1>GetDNSConfig), and the hex letters beside the "::" go with it.
//
// No rule separates the two. In CNSSCertStore::CNSSCertStore, "e::C" is a valid address glued to
// a word character on both sides, so refusing it refuses an address glued the same way. Refusing
// only a bare "::", which carries no bits, was measured and not taken. On a run of three colons
// the match then starts one colon later, and ":::1:2:3:4:5:6:7:8" sends its last group.
//
// A run of three colons is rule 7a's first (#134, ipv6GluedRunPattern). This pattern's bare "::"
// and "c::" end one colon into such a run, and the address after it was sent.
//
// An address ending in a dotted quad is rule 7b's (#49, ipv6DottedQuadPattern), not an alternative
// here. A match of this pattern ends wherever its alternative does, with no context after it, and a
// dotted-quad form ending at the quad would cut a value a later rule reads through the next character
// ("::ffff:10.0.0.5.6", where 8 masks the last four octets after this pattern stops at "10").
//
// internal/anonymize/ipv6_compare_test.go runs both patterns over one table and names each
// difference, so a change here that moves a row fails there.
const ipv6Pattern = `(?i)(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,7}:` +
	`|(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,5}(?::[0-9a-f]{1,4}){1,2}` +
	`|(?:[0-9a-f]{1,4}:){1,4}(?::[0-9a-f]{1,4}){1,3}` +
	`|(?:[0-9a-f]{1,4}:){1,3}(?::[0-9a-f]{1,4}){1,4}` +
	`|(?:[0-9a-f]{1,4}:){1,2}(?::[0-9a-f]{1,4}){1,5}` +
	`|[0-9a-f]{1,4}:(?::[0-9a-f]{1,4}){1,6}` +
	`|:(?:(?::[0-9a-f]{1,4}){1,7}|:)`

// ipv6GluedRunPattern is 7a's source (#134): a "::"-led address glued on by ':', as far as a
// boundary. The group is the run and the groups around it; the boundary after it is context.
// Since #49 the groups after the run may end in a dotted quad (ipv6DottedQuadTail), which is 7b's
// right end too. See the chain entry for why each part is what it is.
const ipv6GluedRunPattern = `(?i)(` + ipv6GluedRunLead + `(?:` + ipv6GluedRunTail + `|` + ipv6DottedQuadTail + `))` + ipv6GluedRunEnd

// ipv6GluedRunLead is the groups before the run, 7's own, then the run of three colons or more.
const ipv6GluedRunLead = `(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*)?:{3,}`

// ipv6GluedRunTail is the groups after the run, separated by single colons. A unit is a hex
// character or one of the pipeline's placeholders, so the Template field's form is read too.
const ipv6GluedRunTail = `(?:` + ipv6GluedRunUnit + `{1,4}(?::` + ipv6GluedRunUnit + `{1,4})*)?`

const ipv6GluedRunUnit = `(?:[0-9a-f]|` + pipelinePH + `)`

// ipv6GluedRunEnd is what may follow: whitespace, closing punctuation, '/' (a prefix length, a
// path), '%' (a zone id), a '.' followed by whitespace or nothing (a sentence's end), or the end of
// the text. Each was admitted only after TestGluedRunMasksASupersetOfMain stayed green with it.
const ipv6GluedRunEnd = `(?:[\s,;)\]}>"'/%]|\.(?:\s|$)|$)`

// ipv6DottedQuadPattern is 7b's source (#49): an IPv6 address whose last 32 bits are written as a
// dotted quad, as far as a boundary. The group is the address; the boundary after it is 7a's, and
// context. The first alternative carries a "::", the second is the full form, six groups or more.
const ipv6DottedQuadPattern = `(?i)(` +
	`(?:(?:[0-9a-f]{1,4}:)*[0-9a-f]{1,4})?::` + ipv6DottedQuadTail +
	`|(?:[0-9a-f]{1,4}:){6}` + ipv6DottedQuadTail +
	`)` + ipv6GluedRunEnd

// ipv6DottedQuadTail is the right end both 7a and 7b take before the boundary: hex groups, each
// followed by ':', then the quad -- detector 8's ipv4Pattern, or the pipeline's <IP> on the Template
// field -- and an optional port. It is ONE const because 7a must take every glued address 7b would
// take one colon later; see 7b.
const ipv6DottedQuadTail = `(?:[0-9a-f]{1,4}:)*(?:` + ipv4Pattern + `|<IP>)(?::[0-9]{1,5})?`

// bareHostPattern is 9b's source: dotted labels, then one of suffixes.
//
// A hostname label is a grammar rather than a flat run — it may not begin or end with a
// hyphen — so tolerance goes on both halves of it rather than through tolerant().
//
// '_' is admitted on both halves since #66, so a label may also begin or end with one. A DNS
// service label begins with one (_dmarc, _sip._tcp), and refusing it would keep sending it;
// refusing a trailing one would buy nothing. What it costs is that \b now starts before a word
// run glued to a host by '_', and a leading-'_' label that is not a host (an mDNS service type
// on .local) is one to this rule -- both pinned in TestAcceptedOverMasking.
//
// A SUFFIX IS READ IN THE FORM THE PIPELINE LEAVES IT IN TOO (#105). The suffixes used to be spliced
// in verbatim, and the labels were tolerant while the suffix was not -- #52's lesson, on a literal
// inside the group this time. No built-in suffix is one the pipeline rewrites, but a configured one
// can be, in more ways than "a digit becomes <NUM>": NUM takes the sign and the decimal tail with it
// (eu-1 and v1.2 become eu<NUM> and v<NUM>), HEX takes a suffix with no digit at all (deadbeef, 0xcafe
// become <HEX>), and a suffix beginning with a digit loses the dot before it when the label before
// it ends in one (db1.2corp becomes db<NUM>corp). So on the Template field 9b went quiet and the
// host was sent.
//
// THE REWRITTEN FORM IS DERIVED, NOT WRITTEN, and that is why this package imports the pipeline.
// A hand-written tolerance would have to copy UUID, HEX with its letter rule, and NUM, in the
// pipeline's order -- a second copy of another package's patterns, which is #42's class. Asking
// pipeline.Templatize instead is exact for every pattern, today's and tomorrow's. The masking
// itself stays separate, as the package comment requires; what is shared is the one fact this rule
// has to agree with the pipeline about, how a suffix looks once rewritten.
//
// Each suffix's rewritten form T is an alternative beside it, and Templatize on the suffix alone
// gives what it gives in a host: 9b demands a boundary after the suffix, so nothing after it can
// join a run inside it, and a NUM that takes a trailing ".5" with it keeps the same prefix. The one
// thing context changes is the dot BEFORE a suffix, and that is measured the same way, on a probe
// label ending in a digit (mergedRest).
//
// A FORM ENDING IN A PLACEHOLDER IS CLOSED BY \B, NOT \b. After '>' a \b demands a word character
// next, the opposite of what the raw line asks of a suffix ending in a digit. '>' is not a word
// character, so \B holds exactly where the next character is not one, or the text ends: the same
// edge \b draws after "corp1". It is zero-width, so the group is still the whole match.
//
// Built-in suffixes, and a configured one the pipeline leaves alone, add no form, and the source is
// main's byte for byte (TestBareHostPatternUnchanged).
func bareHostPattern(suffixes []string) string {
	label, inner := `(?:[a-z0-9_]|`+pipelinePH+`)`, `(?:[a-z0-9_-]|`+pipelinePH+`)`
	labels := `(?:` + label + `(?:` + inner + `*` + label + `)?\.)`

	wordEnd := escapeAll(suffixes)
	var phEnd, merged []string
	for _, s := range suffixes {
		if t, _ := pipeline.Templatize(s); t != s {
			if endsInPipelinePH.MatchString(t) {
				phEnd = append(phEnd, regexp.QuoteMeta(t))
			} else {
				wordEnd = append(wordEnd, regexp.QuoteMeta(t))
			}
		}
		if rest, ok := mergedRest(s); ok {
			merged = append(merged, regexp.QuoteMeta(rest)+closeSuffix("<NUM>"+rest))
		}
	}
	suffix := `(?:` + strings.Join(wordEnd, "|") + `)\b`
	if len(phEnd) > 0 {
		suffix = `(?:` + suffix + `|(?:` + strings.Join(phEnd, "|") + `)\B)`
	}
	pat := labels + `+` + suffix
	if len(merged) > 0 {
		// The last label and the suffix's leading digits are one <NUM>, so there is no dot to
		// find; at least one label character before the <NUM> is required, the label it ate.
		pat = `(?:` + pat + `|` + labels + `*` + label + inner + `*<NUM>(?:` + strings.Join(merged, "|") + `))`
	}
	return `(?i)\b` + pat
}

// endsInPipelinePH matches a rewritten suffix that ends in one of the pipeline's placeholders.
var endsInPipelinePH = regexp.MustCompile(pipelinePH + `$`)

// closeSuffix is the boundary after a suffix form: \b after a word character, as it always was, and
// \B after a placeholder's '>' (see bareHostPattern).
func closeSuffix(form string) string {
	if endsInPipelinePH.MatchString(form) {
		return `\B`
	}
	return `\b`
}

// mergedRest reports what is left of suffix s after the pipeline has merged its leading digits
// with the label before it: db1.2corp templatizes to db<NUM>corp, with no dot left for 9b to find.
// It is measured, on a probe label that ends in a digit, rather than reasoned from the NUM pattern.
//
// It reports nothing unless the rest carries a letter outside a placeholder, which is 9d's rule
// that a name's top label is alphabetic. An all-digit suffix would otherwise make every word glued
// to a number a host.
func mergedRest(s string) (string, bool) {
	p, _ := pipeline.Templatize("x9." + s)
	rest, ok := strings.CutPrefix(p, "x<NUM>")
	if !ok || strings.HasPrefix(rest, ".") || !hasLetter.MatchString(pipelinePHRe.ReplaceAllString(rest, "")) {
		return "", false
	}
	return rest, true
}

var (
	pipelinePHRe = regexp.MustCompile(pipelinePH)
	hasLetter    = regexp.MustCompile(`[A-Za-z]`)
)

// escapeAll quotes each suffix for safe interpolation into the bare-host alternation.
func escapeAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}
