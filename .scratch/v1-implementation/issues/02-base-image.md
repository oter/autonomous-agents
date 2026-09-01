# 02: Base image

**What to build:** One shared container image that every Run uses. Pulling and running it gives a working `claude` and `codex`, the tooling the entrypoint and Teardown need, and nothing that belongs to the control plane.

**Blocked by:** None (can start immediately).

**Status:** ready-for-agent

- [ ] Image contains `claude`, `codex`, the `skills` CLI installed globally (so `npx` never re-downloads it per Run), `curl`, `jq`, `git`, `tar`, `zstd`, `iptables`, `setpriv`
- [ ] Image does NOT contain `age` — decryption is the control plane's job
- [ ] An unprivileged `agent` user exists for the privilege drop
- [ ] `docker run` of the image answers `claude --version` and `codex --version`
- [ ] Image is tagged with a date tag and pushed where the control-plane config's `image` key can reference it
