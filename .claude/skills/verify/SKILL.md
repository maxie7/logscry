---
name: verify
description: Run the full Go verification loop for logscry (format, vet, lint, test, build). Use before finishing any task or committing.
---
Run these in order and report pass/fail for each; stop and fix on the first failure:
1. `gofmt -l .`  (must print nothing; if it lists files, run `gofmt -w .` and re-check)
2. `go vet ./...`
3. `golangci-lint run`
4. `go test ./...`  (CI's coverage step: every test, the anonymizer's superset gates included)
5. `go test -race -skip "$(sed -n "s/^ *SUPERSET_GATES: '\(.*\)'$/\1/p" .github/workflows/ci.yml)" ./...`
   (CI's race step: the concurrency model is load-bearing: pipeline goroutine owns the template
   map, the pool never blocks ingest. The superset gates are skipped: they are pure (each gate
   and its two subtests run in parallel, every input gets fresh Mappers, and the corpus a gate
   built is only read, so the detector has nothing to find in them), and under -race they
   exceed the default 10-minute package timeout)
6. `go build ./...`

