# 07: Webhook Triggers

**What to build:** An external service POSTs to an Agent's webhook path on the public hooks listener; a verified request starts a Run whose payload endpoint serves the raw body, and an unverified one starts nothing. This is the internet-facing surface, guarded solely by per-Agent auth.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Hooks listener serves each Agent's declared webhook path; unknown paths 404
- [ ] `hmac_sha256` verification with configurable header and hex/base64 encoding, constant-time compare; `bearer` verification; `none` accepted only when declared
- [ ] Auth secrets are age-decrypted via the same master identity as ticket 06; decrypted webhook secrets never appear in logs or responses
- [ ] The public hooks listener serves ONLY `/hooks/*` — no run, UI, health, or debug routes are reachable on it, and auth-failure responses reveal nothing beyond the status code (no expected-signature echoes, no Agent enumeration)
- [ ] The raw request body is stored as the Run's Payload and served verbatim by the payload endpoint — no templating, no parsing on the control plane's side
- [ ] Demo: a correctly signed request (Linear/GitHub-shaped) starts a Run that reads its trigger from the payload; a tampered body or bad signature is rejected and starts nothing
