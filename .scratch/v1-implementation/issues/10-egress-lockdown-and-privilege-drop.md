# 10: Egress lockdown + privilege drop

**What to build:** A Run reaches the internet and the control plane's one port, and provably nothing else — not the rest of the tailnet, not RFC1918. The agent process cannot undo the rules because it no longer holds the capability that installed them.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Container created with `NET_ADMIN`; entrypoint installs the SPEC §7 iptables rules INSIDE the container (the host `DOCKER-USER` route breaks on macOS, where Docker's chains live in a VM — in-container rules behave identically on both Runners)
- [ ] Rules: allow internet, allow control-plane tailnet IP on one port, drop the rest of 100.64.0.0/10, drop 10/8 + 172.16/12 + 192.168/16
- [ ] Agent process runs as the unprivileged user via `setpriv` with groups cleared, so it cannot flush the rules; Teardown remains in the root shell and can still push after the agent wedges itself
- [ ] Demo from inside a Run as the agent user: internet reachable, control plane reachable, another tailnet IP and an RFC1918 address both provably blocked, and an attempt to flush iptables fails
