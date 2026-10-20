# Security policy

## Supported versions

Only the [latest release](https://github.com/maxie7/logscry/releases/latest) is supported.
A fix ships as a new patch release; earlier releases are not patched.

## Reporting a vulnerability

Report it privately through GitHub's private vulnerability reporting:
**<https://github.com/maxie7/logscry/security/advisories/new>**. Please do not open a public
issue, and do not describe the problem in a pull request or a discussion. A public report
discloses the problem before it is fixed.

**When in doubt, report privately.** A report that turns out to belong in a public issue costs
nothing; one made in public by mistake discloses.

Please include:

- the output of `logscry --version`;
- the full command line, every flag included, and the relevant keys if you use `--config`;
- the model endpoint kind: local (Ollama or another endpoint on the same machine), remote, or
  `--explain-dry-run`;
- a minimal **synthetic** reproduction: an invented log line of the same shape, and what reached
  the model endpoint (or what logscry did).

**Never attach real logs, journald or Docker captures, or live credentials**, not even in a
private report. Replace every real value with an invented one of the same shape. If a real
credential was sent somewhere it should not have been, rotate it first.

## What to expect

logscry has a single maintainer, and security reports are handled on a best-effort basis. There
is no SLA, no promised response or fix timeline, and no bounty. A confirmed vulnerability is
fixed in a new patch release. As with earlier anonymizer fixes, the README then says which
releases were affected and what was sent.

## In scope

Each item below is something the [README](README.md) says logscry does, or, for the last item,
how it is built.

- **A value `--llm-anonymize` is documented to mask reaches the model endpoint**, in whole or in
  part, in a shape the README does not already list as open. "Documented to mask" means the
  "What it masks" list in [Anonymization](README.md#anonymization---llm-anonymize). A partial
  mask counts: a new shape in which part of a documented value gets through is exactly what
  this policy is for.
- **A request reaches a model endpoint despite `--explain-dry-run`.**
  [Configuration](README.md#configuration) says dry-run "builds no backend and no worker pool —
  so no request *can* be made", and [Replay](README.md#replay---replay-file) relies on that.
- **A request is sent for an escalation whose masking failed.**
  [Anonymization](README.md#anonymization---llm-anonymize) says that if masking a payload fails,
  the escalation "is **skipped** (the card says so) rather than sent in the clear". That
  includes a host list longer than the 64-member bound. The same section states what the
  fail-closed re-scan cannot see; a leak of that kind belongs under the first item.
- **A release artifact that fails verification.** Any archive or `checksums.txt` for which
  `gh attestation verify <file> --repo maxie7/logscry` fails, or an archive whose SHA-256 does
  not match `checksums.txt` ([Installation](README.md#installation)).
- **A crafted log line or stream that crashes logscry, hangs it, or makes it execute
  anything.** logscry reads log lines as data. The only programs it starts are the command
  given after `--` and `journalctl` for [`--journald`](README.md#journald---journald-linux-only),
  and the model stage never blocks ingestion ([How it works](README.md#how-it-works)).

## Out of scope

Please do not report these privately:

- **Real values in the TUI and in the `--export` file.** That is by design: with
  `--llm-anonymize` "the terminal and the cards always show the real values"
  ([Anonymization](README.md#anonymization---llm-anonymize)), and "The values are real, even
  with `--llm-anonymize`" ([Export](README.md#export---export-path)).
- **Unmasked lines sent to a local Ollama.** Nothing leaves the machine, and the README says the
  flag is unnecessary there ([Anonymization](README.md#anonymization---llm-anonymize)).
- **Unmasked lines sent to a remote endpoint with `--llm-anonymize` off.** Sending lines
  verbatim is the documented default, and logscry prints a one-line notice at startup when
  `--llm-url` points at a remote provider
  ([Anonymization](README.md#anonymization---llm-anonymize)).
- **Gaps the README already names.** These are every item in
  [Known limitations](README.md#known-limitations-v1), and every open item in
  [Anonymization](README.md#anonymization---llm-anonymize) under "What the audit found and did
  not close", including the shapes that section names inline as not reached. They are public
  issues already, so add a comment to the issue instead.
- **Public hostnames in prose.** The README leaves them unmasked deliberately.
- **A secret format no detector targets.** Ask for it in a public enhancement request; #116 is
  the model for one.
