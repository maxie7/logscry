---
name: verify
description: Run the full Go verification loop for logscry (format, vet, lint, test, build). Use before finishing any task or committing.
---
Run these in order and report pass/fail for each; stop and fix on the first failure:
1. `gofmt -l .`  (must print nothing; if it lists files, run `gofmt -w .` and re-check)
2. `go vet ./...`
3. `golangci-lint run`
4. `go test ./...`
5. `go build ./...`

