# 02: Base image

**What to build:** One shared container image that every Run uses. Pulling and running it gives a working `claude` and `codex`, the tooling the entrypoint and Teardown need, and nothing that belongs to the control plane.

**Blocked by:** None (can start immediately).

**Status:** done (image pushed by CI on a date tag)

- [x] Image contains `claude`, `codex`, the `skills` CLI installed globally (so `npx` never re-downloads it per Run), `curl`, `jq`, `git`, `tar`, `zstd`, `iptables`, `setpriv`
- [x] Image does NOT contain `age` — decryption is the control plane's job
- [x] An unprivileged `agent` user exists for the privilege drop
- [x] `docker run` of the image answers `claude --version` (2.1.252) and `codex --version` (0.152.0)
- [x] Image is tagged with a date tag and pushed where the control-plane config's `image` key can reference it — CI (`.github/workflows/base-image.yml`) builds multi-arch (amd64 + arm64) and pushes `ghcr.io/oter/agent-base:<date>` whenever a date-shaped git tag (e.g. `2026-09-01`) is pushed

Built from `image/Dockerfile`: `node:24-trixie-slim` (Debian 13 with the
official Node 24 LTS; trixie's own apt Node is frozen at 20, below the
`>= 22` the CLIs declare), the Go toolchain copied from `golang:1.27-trixie`,
`build-essential` with `pkg-config` and `python3` for native builds, and yarn
through corepack so a repo's `packageManager` pin is honoured. The CLIs are
npm packages. Tag `2026-09-03` is the first with this base and the ticket 03
entrypoint.
