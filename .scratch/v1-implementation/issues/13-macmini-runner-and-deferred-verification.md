# 13: macmini Runner + the deferred verification

**What to build:** The second Runner works: an Agent declaring `runner: macmini` runs on the remote host through the autossh-forwarded unix socket, and the control plane branches on nothing. Then run the verification SPEC §12 deliberately deferred to now.

**Blocked by:** 03 (Walking-skeleton Run), 10 (Egress lockdown + privilege drop).

**Status:** ready-for-agent

- [ ] autossh sidecar forwards the remote Docker socket to a shared path with the SPEC §3 flags (`-M 0`, keepalives, `ExitOnForwardFailure`); the Docker SDK's `ssh://` is NOT used — the SDK has no such support and silently TCP-dials the literal string
- [ ] The macmini runner config is just another unix socket; the spawn path is byte-identical to `local`
- [ ] THE load-bearing test: `docker stop --time=30` through the forwarded socket delivers SIGTERM, the trap completes, exit 0 not 137 — if this fails, Teardown never fires on a timeout kill there
- [ ] Verified: `NET_ADMIN` + in-container iptables behave the same inside the macmini VM (Docker Desktop or Colima)
- [ ] Runner host pinned to Docker Desktop ≥ 4.88.0 or Colima (Desktop truncates shutdown grace to ~2s below that, losing in-flight Journals)
- [ ] Findings that contradict the spec produce macmini-local rework recorded here, not a redesign of `local`
