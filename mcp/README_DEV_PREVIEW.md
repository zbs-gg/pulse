# Pulse Personal MCP development note

The public product is the `@zbs-gg/pulse` package. This directory is its local
MCP component, not another product and not a separately published package.

Run `npm test` and `npm run build` here. Run `make verify` at repository root
before handing off executable changes.

Normal harness connections use `stdio`. Loopback HTTP exists for development
tests only. There is no remote memory service in Pulse Personal.
