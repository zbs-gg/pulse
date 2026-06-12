# Install Pulse With Your AI Agent

Pulse is meant to be installed trust-first: your agent audits before
anything is written. The agent-facing script is [`AGENTS.md`](../AGENTS.md)
— audit → explain → confirm → install the Local Preview → `pulse doctor` →
`pulse demo` (or say plainly that the machine only supports Safe Mode).

## Copyable prompt

The maintained copy of this prompt lives in the repo root
[README](../README.md#copy-this-to-your-agent). Short form:

```text
Please check whether it is safe to install Pulse:
https://github.com/zbs-gg/pulse
Read README.md, AGENTS.md, llms.txt (written for you). Verify
npm view @zbs-gg/pulse dist-tags shows preview >= 0.6.0. Explain what an
install writes and how to erase it. Ask my confirmation, then:
npx @zbs-gg/pulse@preview init claude-code
Run pulse doctor and tell me honestly which mode this machine gets; if full
retrieval is on, run pulse demo and narrate the three states + reasons +
continuity pack. Never present the fallback as the Pulse engine. No old-chat
import, no raw transcripts, no secrets.
```

Security checklist for the agent: [`SECURITY_INSTALL_CHECKLIST.md`](SECURITY_INSTALL_CHECKLIST.md).
