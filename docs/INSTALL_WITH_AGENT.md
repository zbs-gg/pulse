# Install Pulse With Your AI Agent

Pulse is meant to be installed trust-first: your agent audits before
anything is written. The agent-facing script is [`AGENTS.md`](../AGENTS.md)
— audit → explain → confirm → install the Local Preview → `pulse doctor` →
`pulse demo` (or say plainly that the machine only supports Safe Mode).

## Copyable prompt

The maintained copy of this prompt lives in the repo root
[README](../README.md#copy-this-message-to-your-ai-agent). Short form:

```text
Hi. Please check whether it is safe to install Pulse:
https://github.com/zbs-gg/pulse

Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
Check: npm view @zbs-gg/pulse dist-tags
Explain which harness path fits my setup, what Pulse writes, what it will not
do by default, and how I can erase it. Ask my confirmation before installing.

For Claude Code full local preview:
  npx @zbs-gg/pulse@preview init claude-code
  pulse doctor
  pulse demo

For other MCP-compatible hosts:
  configure the host to run:
  npx -y @zbs-gg/pulse@preview mcp
  and say plainly that this is Safe Mode/fallback, not the full state-aware
  Pulse engine.

No old-chat import without separate confirmation. No raw transcripts. No
secrets in output. Stop and explain if anything looks unsafe.
```

Security checklist for the agent: [`SECURITY_INSTALL_CHECKLIST.md`](SECURITY_INSTALL_CHECKLIST.md).
