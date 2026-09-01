# 14: Deploy to Coolify

**What to build:** The control plane runs for real in Coolify: webhooks arrive from the internet, the UI and Run API are reachable only over the tailnet, and a config change ships by redeploy — which IS the reload. The "it's real" ticket.

**Blocked by:** 07 (Webhook Triggers), 12 (Read-only UI).

**Status:** ready-for-agent

- [ ] Control plane containerized and deployed via Coolify with the agents dir and config from git
- [ ] Listener split verified in production: hooks listener publicly reachable; UI and Run listeners unreachable from the public internet (probe from outside the tailnet and get nothing) — secrets and Run data never traverse the public surface
- [ ] Master identity mounted 0600 into the control plane only; never baked into an image or committed
- [ ] Coolify's stop honors `stop_grace` so an in-flight redeploy doesn't kill Runs' Teardown mid-upload
- [ ] A malformed Agent YAML pushed to git fails the deploy loudly (startup validation from ticket 01 doing its job in production)
- [ ] Demo: a real external webhook (Linear or GitHub) triggers a Run end to end in the deployed system
