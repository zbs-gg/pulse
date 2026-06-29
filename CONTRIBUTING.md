# Contributing to Pulse

Thanks for looking under the hood.

## Verify before you push

```bash
make verify   # go build + vet + gofmt + test (pulse-app/), plus mcp test + build
```

Exit 0 means the gate passed.

## Pull requests

- PRs are reviewed by a human. **Auto-merge is disabled** — every PR waits for
  a person, by design.
- Keep changes scoped. Note any change to install behavior, network paths, or
  default model/embedder explicitly in the PR description.
- Do not commit personal data, private history, secrets, or internal paths —
  this repository is public.

## Honesty canon

Pulse copy never presents Safe Mode (keyword recall) as the full engine, never
puts benchmark numbers next to the fallback, and never claims production
readiness. See `AGENTS.md`.
