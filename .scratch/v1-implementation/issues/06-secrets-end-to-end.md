# 06: Secrets end to end — Broker endpoint + dsecrets

**What to build:** An Agent's `secrets` map works as the Allowlist. Inside a Run, `dsecrets NAME -- cmd` gets exactly the named secrets into the child's environment; asking for anything outside the Allowlist is a loud, named refusal. Respects ADR-0003 (secrets over the Run API) and ADR-0001/0002 (broker + envelope).

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Control plane loads the age master identity from the configured path (0600) and decrypts Allowlist values on demand — never at startup, never into the container image
- [ ] Secrets endpoint: allowed names return the decrypted map; any denied name is a 403 naming the denied names, never a silent omission
- [ ] `dsecrets` ships in the base image with the exact SPEC §8 script semantics — each line there is measured: `exec` not fork (sops exec-env orphans its child on SIGTERM, which here loses the Journal of every timed-out Run), `has($n)` before reading the value (command substitution exit status is its last command), `jq -j` + sentinel for byte-exact values
- [ ] No file sink, no decrypt-everything mode; the secret name IS the environment variable name
- [ ] The model credential the CLI itself consumes is plaintext in the agent's environment — the accepted hole, documented, not "fixed"
- [ ] YAML docs/examples use `|` literal blocks for age ciphertext (folded scalars mangle armor)
- [ ] The secrets endpoint is served ONLY on the tailnet-bound run listener — it must be unreachable from the public hooks listener, verified by a test that requests it there and gets nothing
- [ ] Decrypted secret values never appear in control-plane logs, error messages, or any response other than the secrets endpoint's success body; denials name secret NAMES only, never values
- [ ] Demo: an allowed name round-trips through `dsecrets`; a name outside the Allowlist fails with the 403 naming it
