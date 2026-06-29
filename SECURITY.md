# Security

Pulse is local-first and designed to be auditable before you run it.

- Backend model calls are **off by default**. The optional Cohere *embedding*
  path is the only outbound call, and `pulse doctor` reports it.
- Raw transcript capture is **off by default**; the store rejects
  transcript-, secret-, and path-like content.
- Memory is local and inspectable. Destructive wipe requires the exact
  confirmation phrase.

Full install-time checklist: [docs/SECURITY_INSTALL_CHECKLIST.md](docs/SECURITY_INSTALL_CHECKLIST.md).

## Reporting a vulnerability

Email **team@zbs.gg** with details and a reproduction. Please do not open a
public issue for security reports.
