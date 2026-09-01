# 02: Base image

**What to build:** One shared container image that every Run uses. Pulling and running it gives a working `claude` and `codex`, the tooling the entrypoint and Teardown need, and nothing that belongs to the control plane.

**Blocked by:** None (can start immediately).

**Status:** done (push happens in CI on merge to main)

- [x] Image contains `claude`, `codex`, the `skills` CLI installed globally (so `npx` never re-downloads it per Run), `curl`, `jq`, `git`, `tar`, `zstd`, `iptables`, `setpriv`
- [x] Image does NOT contain `age` — decryption is the control plane's job
- [x] An unprivileged `agent` user exists for the privilege drop
- [x] `docker run` of the image answers `claude --version` (2.1.252) and `codex --version` (0.152.0)
- [x] Image is tagged with a date tag and pushed where the control-plane config's `image` key can reference it — CI (`.github/workflows/base-image.yml`) builds multi-arch (amd64 + arm64) and pushes `ghcr.io/oter/agent-base:<date>` on every merge to main that touches `image/`

Built from `image/Dockerfile` (debian:trixie-slim; nodejs from apt, the CLIs are npm packages).
