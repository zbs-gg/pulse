# Pulse Developer Preview — pointer

The canonical preview story lives at the repo root: [README](../README.md)
(product, install matrix, Safe Mode boundary) and [AGENTS.md](../AGENTS.md)
(the script an AI agent follows to vet and install).

One package: `@zbs-gg/pulse` (preview dist-tag, v0.6.0+). The public path is
Pulse Local Preview — `npx @zbs-gg/pulse@preview init claude-code`, then
`pulse doctor`, then `pulse demo`. Safe Mode (`claude mcp add pulse -- npx -y
@zbs-gg/pulse@preview mcp`) is the fallback, never the product, and carries
no benchmark claims.

## Release gate (run fresh before sharing)

```bash
cd pulse-app
env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u COHERE_API_KEY go test ./...

cd cli
npm test
npm pack --dry-run

cd ../../mcp
npm test
npm run build
```

Plus the live proof on a full-retrieval machine: `pulse demo` must show three
different top-3 sets across the three states with visible reasons, and
`pulse doctor` must print the honest verdict line for the machine's mode.

Boundaries: [SAFE_CLAIMS](docs/developer-preview/SAFE_CLAIMS.md) (incl. hard
rejection criteria), [KNOWN_LIMITATIONS](docs/developer-preview/KNOWN_LIMITATIONS.md),
[UNINSTALL_AND_WIPE](docs/developer-preview/UNINSTALL_AND_WIPE.md).
