# Native helper security boundary

The helper no longer accepts arbitrary bytes for DPoP signing. It constructs
the proof itself from an exact structured request and pins each key to one Team
resource, OAuth token endpoint, client ID, and subject. The remote token profile
forbids write, delete, and owner scopes.

The Secure Enclave protects private-key extraction, not a logged-in macOS user
from every process running as that same user. A same-UID process may still be
able to read the OAuth credential through the user's Keychain, invoke the
bounded helper proof API, or delete helper metadata/key material. Existing
pre-v2 keys are bound once to constraints from the already validated credential
document; a malicious same-UID process racing that one-time migration remains
outside this preview's isolation guarantee.

Full same-UID malware isolation requires a separately authorized always-on
service or equivalent OS-enforced client attestation. Until that architecture
exists and is tested, same-UID isolation is a release blocker for any claim
stronger than "non-exportable keys and read-scope-bounded signing."
