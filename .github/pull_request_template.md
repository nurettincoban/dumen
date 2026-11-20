## What changed and why

## Gauntlet

See [docs/gauntlet.md](../docs/gauntlet.md). Tick a stage when it passed; write "not run" and why when it was skipped.

- [ ] 1. Correctness and concurrency: `go vet ./...` and `go test -race -short ./...`
- [ ] 2. Security and agent bypass: bypass attempts for every guard this change touches
- [ ] 3. Claude Code conformance: hooks reference checked, `claude plugin validate .`, e2e when hooks or MCP changed
- [ ] 4. Cross-platform and host independence: builds for linux, darwin, windows
- [ ] 5. Open source and data model: version gate and migration note for layout changes, README still true

Open findings (Critical and High block the merge):

| Severity | Finding | Status |
| --- | --- | --- |
