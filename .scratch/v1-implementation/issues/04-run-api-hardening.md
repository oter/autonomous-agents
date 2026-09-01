# 04: Run API hardening — heartbeats, staleness, throttling

**What to build:** The Run API becomes the trustworthy channel of SPEC §9. A healthy Run heartbeats and stays green; a silent Run gets checked against Docker rather than guessed about; an abusive Run gets throttled and flagged but never killed by the API.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Entrypoint heartbeats status every 30s; three missed heartbeats mark the Run stale and trigger an immediate ContainerInspect — a gap is a hint to ask Docker, never a conclusion
- [ ] First terminal state wins; ContainerInspect is authoritative on disagreement
- [ ] Full status semantics: 401 absent/bad token, 403 on a terminal Run, 404 unknown Run
- [ ] Per-Run token bucket returns 429 when exceeded; throttle events are recorded on the Run (surfaced later in UI and Journal); nothing auto-kills
- [ ] A Run that loses the control plane keeps running and can finish its work without help; only secret-needing commands fail until the link returns
- [ ] Governing principle enforced in routing: the Run API is read-only about the Run itself and write-only about its own status — no route lists Agents, touches config, or reaches another Run
