# Pulse for BB

Local Personal memory using the installed signed Pulse runtime. Requires the
complete-moment API in Personal 0.8.3 and the ordinary signed workspace binding.
The BB plugin does not enroll projects or alter the vault during a build.

`pulse_memory` accepts every meaningful item in a moment, including multiple
emotions with human names, causes and explicit/inferred provenance. Item count
is not a selection rule. The installed runtime validates and constructs the
complete set; `/memory/moments` accepts it durably before materialization.
`stored` requires a terminal object receipt for every part. A missing reply
allows one bounded replay of the identical request; validation errors do not.

`pulse_context` returns scoped relevant memory with moment references.
`pulse_moment` reads the full moment, following `next_cursor`; `status=true`
checks write receipts. CLI equivalents: `bb pulse recall`, `bb pulse remember`
and `bb pulse moment <JSON>`. Project and turn identity come from BB, never the
model. Raw chat, transcripts, secrets and paths are not captured.

Build: `npm ci --ignore-scripts` then `bb plugin build .`.
Test from the repository root with `make verify`, including the BB contract
and scope tests against temporary stores.
Install the built plugin with BB after updating the signed Personal runtime.
