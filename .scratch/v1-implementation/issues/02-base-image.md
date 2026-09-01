# 02: Base image

**What to build:** One shared container image that every Run uses. Pulling and running it gives a working `claude` and `codex`, the tooling the entrypoint and Teardown need, and nothing that belongs to the control plane.

**Blocked by:** None (can start immediately).

**Status:** done (push pending — needs registry auth)

- [x] Image contains `claude`, `codex`, the `skills` CLI installed globally (so `npx` never re-downloads it per Run), `curl`, `jq`, `git`, `tar`, `zstd`, `iptables`, `setpriv`
- [x] Image does NOT contain `age` — decryption is the control plane's job
- [x] An unprivileged `agent` user exists for the privilege drop
- [x] `docker run` of the image answers `claude --version` (2.1.252) and `codex --version` (0.152.0)
- [ ] Image is tagged with a date tag and pushed where the control-plane config's `image` key can reference it — tagged locally as `ghcr.io/oter/agent-base:2026-09-01`; `docker push` requires the operator's ghcr credentials

Built from `image/Dockerfile` (node:22-slim base; both CLIs are npm packages).
