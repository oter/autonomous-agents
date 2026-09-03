# 06: Secrets end to end — Broker endpoint + dsecrets

**What to build:** An Agent's `secrets` map works as the Allowlist. Inside a Run, `dsecrets NAME -- cmd` gets exactly the named secrets into the child's environment; asking for anything outside the Allowlist is a loud, named refusal. Respects ADR-0003 (secrets over the Run API) and ADR-0001/0002 (broker + envelope).

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** resolved

- [x] Control plane loads the age master identity from the configured path (0600) and decrypts Allowlist values on demand — never at startup, never into the container image
- [x] Secrets endpoint: allowed names return the decrypted map; any denied name is a 403 naming the denied names, never a silent omission
- [x] `dsecrets` ships in the base image with the exact SPEC §8 script semantics — each line there is measured: `exec` not fork (sops exec-env orphans its child on SIGTERM, which here loses the Journal of every timed-out Run), `has($n)` before reading the value (command substitution exit status is its last command), `jq -j` + sentinel for byte-exact values
- [x] No file sink, no decrypt-everything mode; the secret name IS the environment variable name
- [x] The model credential the CLI itself consumes is plaintext in the agent's environment — the accepted hole, documented, not "fixed"
- [x] YAML docs/examples use `|` literal blocks for age ciphertext (folded scalars mangle armor)
- [x] The secrets endpoint is served ONLY on the tailnet-bound run listener — it must be unreachable from the public hooks listener, verified by a test that requests it there and gets nothing
- [x] Decrypted secret values never appear in control-plane logs, error messages, or any response other than the secrets endpoint's success body; denials name secret NAMES only, never values
- [x] Demo: an allowed name round-trips through `dsecrets`; a name outside the Allowlist fails with the 403 naming it

## Answer

One new file in `internal/run`, one script in the image, one new dependency
(`filippo.io/age`, the map's charting pick; `armor` with it). The Broker of
the glossary is the control plane's own secrets endpoint, as ADR-0003 says.

- **Master identity** (`internal/run/secrets.go`): `run.MasterIdentity` is
  the configured path. It is read at every use and held no longer:
  `Check()` at startup reads it once so a deploy whose key file is missing,
  readable by others (anything looser than 0600), or malformed fails before
  it serves; `Decrypt` reads it again for each value. Nothing is decrypted
  at startup, and the identity never enters the image or a container.
- **Config** (`internal/config/load.go`): `secrets.master_identity` is
  required. Each secret name must be an environment variable name, since
  that is what `dsecrets` exports it as; each value must decode as age
  armor, a check that needs no key, so a `>` folded block — which joins the
  armor lines — fails startup naming the key and hinting `|`.
- **`POST /run/secrets`** (`internal/run/api.go`): the Run record now
  carries `Secrets`, the Agent's Allowlist as ciphertext, from spawn. Any
  requested name outside it is a 403 `{"denied":[...]}` naming every denied
  name, and nothing is decrypted for that request; otherwise each value is
  decrypted now and returned as `{NAME: value}`. Grants are logged at info
  and denials at warn, with the Run, the Agent, and the names; a value
  appears in the success body and nowhere else, and a decrypt failure is a
  500 naming the name. `GET` is 405; the routing probe still 404s
  everything else.
- **Spawner** (`internal/run/spawn.go`): the model credential —
  `CLAUDE_CODE_OAUTH_TOKEN` for claude, `CODEX_API_KEY` for codex — is the
  one Allowlist value decrypted at spawn, into the container's environment
  (SPEC §8's accepted hole). Ticket 03's pass-through from the control
  plane's own environment is deleted, not kept as a fallback: a test sets
  a token in the environment and asserts the Allowlist's value arrives and
  the environment's does not. A credential that does not decrypt fails
  `Start` before any container exists, naming the secret and never its
  value. An Agent that declares none runs unauthenticated, as before, and
  its Journal says so.
- **Hooks listener** (`cmd/control-plane/main.go`): `listen.hooks` is now
  served, by `hooks()`, an empty mux ticket 07 fills, so SPEC §3's three
  listeners all exist. The test builds the binary, starts it on three
  ephemeral ports with a real 0600 key file, and asks the public listener
  for every Run API path: 404 each time, while the run listener answers
  the same requests with the Run API's own 401. The Run API is mounted on
  `listen.run` alone, and that is checked in the wiring, not in a stand-in.
- **`dsecrets`** (`image/dsecrets.sh`, baked at `/usr/local/bin/dsecrets`):
  SPEC §8's script with its measured lines intact — `exec`, `has($n)`
  before the value, `jq -j` plus the sentinel — and two changes the
  ticket's own bullets forced, both measured here. `-f` became
  `--fail-with-body`: with `-f`, curl discards a 403's body, so the denial
  would have been `curl: (22)` and never named a name. `--retry 3` was
  added for ticket 04's 429 follow-up, and measured to leave the refused
  attempt's body in front of the reply on stdout; every reply of this
  endpoint is one line, so the reply is taken as the last line. The
  refusal is `dsecrets: request failed: {"denied":["NAME"]}`, exit 3.
- **Docs**: SPEC §2 (example names, the `|` rule now checked at startup,
  the credential and its source), §3 (identity read at every use,
  required), §5 step 4, §8 (script and bullets, the accepted hole with the
  corrected name, access logged and values not), §9 (nothing decrypted for
  a denied request). ADR-0003 gained an amendment. The sandbox mounts
  `sandbox/age-master.key` with `create_host_path: false` so a missing key
  is an error rather than a directory, drops the credential pass-through,
  and its README has a "Secrets" section with the `printf %s | age -r -a`
  recipe and a `dsecrets` demo Agent in `|` blocks.

### Tests

Unit (`go test ./...`): identity round trip, wrong key, garbage, and the
refusals (0640, empty, malformed, missing); the config checks (bad name,
folded block with the `|` hint, plaintext refused without repeating it,
identity required, armor kept verbatim); the endpoint end to end through the middleware with the log
captured — both names present, neither value; the 403 naming both denied
names and carrying no allowed value; `[]` is 200 `{}`; 400; 405; the spawn
environment carrying the Allowlist's credential and neither the
environment's nor `ANTHROPIC_API_KEY` nor a non-credential secret; the
undecryptable credential creating no container; the binary's three
listeners. The script is tested from Go against a fake of the endpoint
(`image/dsecrets_test.go`): values byte-exact with newlines, spaces, `=`
and non-ASCII, an unasked Allowlist name absent from the child, a denial
named with no child run and no value, the control plane gone, the missing
`--`, the child's pid equal to the script's (exec, not fork), a 429 then
success in two requests, and a 429 then a denial still named.

### Demo (2026-09-03, OrbStack, `autonomous-agents/agent:dev` rebuilt with `dsecrets`, no subscription token in this shell)

`AA_IMAGE=autonomous-agents/agent:dev go test ./internal/run -run WalkingSkeleton -v`
passes its four cases in about 20s and skips one:

- trivial claude Run: exit 1, `api_error`, `journal uploaded` — no
  credential, because none was in the shell to encrypt into the Allowlist;
  the same outcome as ticket 05's demo, now for the documented reason.
- codex Run with `wall_clock: 5s`: exit 143 and its Journal, unchanged.
- `dsecrets` from the real image against the real Run API, with a Run
  registered in the store whose Allowlist holds `DEMO_SECRET`: the allowed
  name arrives in the exec'd child byte-exact; asking for
  `DEMO_SECRET,AWS_SECRET` exits 3 with
  `dsecrets: request failed: {"denied":["AWS_SECRET"]}`, the child never runs,
  and no value is in the output.
- `a Run's agent reaches a secret through dsecrets` — a real claude Run
  whose prompt uses `dsecrets` from its shell — is written and guarded by
  `CLAUDE_CODE_OAUTH_TOKEN`; skipped here, to run once with the token.
- the orphan case, unchanged.

Then the real binary in the sandbox: `age-keygen -o sandbox/age-master.key`,
a throwaway codex Agent with `CODEX_API_KEY` and `DEMO_SECRET` encrypted to
it, `docker compose up --build`. The key came through the bind mount at
0600 and `Check` passed; three listeners came up; run-now started the Run,
whose environment (`docker inspect`) held `CODEX_API_KEY` and no
`DEMO_SECRET`. With the Run's own token borrowed from that environment,
`docker run --entrypoint dsecrets` against `:8082` printed
`[marmalade-7f3a\n]` for the allowed name and
`dsecrets: request failed: {"denied":["AWS_SECRET"]}` with exit 3 for the pair;
the control plane log had `secrets granted … names=[DEMO_SECRET]` and
`secrets denied … denied=[AWS_SECRET]` and no value. `down -v`, Agent
removed; the key stays, gitignored.

### Deliberate shortcuts

- `dsecrets` takes the last line of curl's stdout as the reply. This holds
  while every reply of the endpoint is one line — JSON, or `http.Error`
  text — which it is. Splitting a `-w '%{http_code}'` off the body is the
  upgrade if a multi-line reply ever appears.
- Denied names come back in request order, repeated if asked for twice.
- The armor of every secret is decoded on every startup; microseconds.
- `hooks()` is an empty mux until ticket 07: the smallest thing that makes
  the isolation testable now.

### Follow-ups for later tickets

- Ticket 07 routes into `hooks()` and decrypts `auth.secret` with
  `run.MasterIdentity.Decrypt`; the loader does not armor-check trigger
  secrets yet (its tests use `xx`).
- Ticket 10's privilege drop needs nothing from `dsecrets`: it runs as the
  agent user and holds no capability.
- The published image with `dsecrets` is `ghcr.io/oter/autonomous-agents/agent:2026-09-07`,
  the date tag pushed with this commit; a deploy points at it before an
  Agent uses `dsecrets`.
- Run the guarded LLM case once with the subscription token:
  `CLAUDE_CODE_OAUTH_TOKEN=… AA_IMAGE=autonomous-agents/agent:dev go test ./internal/run -run WalkingSkeleton -v`.
- A codex subscription equivalent of the token is still unresolved;
  `CODEX_API_KEY` is what the Allowlist carries.

### Review (two-axis, Standards and Spec, before commit)

Acted on:

- **A plaintext value in the startup error** (Standards, Spec): the armor
  check wrapped age's error, which quotes the offending first line — for a
  value pasted in plaintext, the value itself — into `log.Error("config")`,
  the exact mistake the check exists to catch. The wrap is gone; the error
  names the key and hints `|`; the test asserts the value is absent.
- **The hooks-listener test was a stand-in** (Spec): it called `hooks()`
  in-process, which could not catch the Run API being mounted on
  `listen.hooks` in `main`. Replaced by the process-level test above.
- **500 was off the §9 contract** (Spec): a value that does not decrypt is
  a 500 naming the name; §9 now lists it.
- `ponytail:` markers (Standards) on the last-line rule in `dsecrets` and on
  `hooks()`; the script's refusal reads `request failed`, true of a denial,
  an exhausted throttle, a 500, and an unreachable control plane alike,
  where `refused` was not; test comments cite SPEC §8/§9; the API comment
  names the route as the glossary's Broker.

Dismissed, with the reason:

- *The identity read at every use is unasked, and the SPEC and ADR were
  amended to match* (Spec). The ticket's sentence reads either way. Between
  uses no key material sits in memory, and the 0600 rule is enforced at
  each use rather than once; the amendments record the choice. A runtime
  removal of the key file breaks `dsecrets` loudly and locally, the same
  failure mode ADR-0003 accepts for an unreachable control plane.
- *Startup armor and name checks, and `master_identity` required, are
  unasked* (Spec). SPEC §2's rule that a malformed Agent fails startup
  covers the first two, and this ticket's `|` bullet is enforced by nothing
  else; a control plane without a master identity cannot authenticate one
  CLI.
- *`--retry 3` also retries a 500* (Spec, Standards): four decrypt attempts
  and four log lines for a value encrypted to the wrong key, which stays
  wrong until a redeploy. curl's transient list is fixed and `--retry` is
  what honours `Retry-After`; a shell loop would be the price of saving a
  misconfigured Agent seven seconds.
- *`fakeSecrets` duplicates the endpoint's allow/deny shape* (Standards).
  The fake owns the 429 knob and answers a script test; the real endpoint
  meets the real script in the walking skeleton.
- *`Store, Bucket, Identity, Log` are a data clump* (Standards): the
  `Spawner` precedent from tickets 03–05.
- *`MasterIdentity` is a string path and `Secrets` a bare map* (Standards):
  the comments name the path and the Allowlist; a type would carry nothing
  more yet.
- *The model-driven demo did not run* (Spec): no subscription token in
  this shell. Written, guarded, and listed under follow-ups.
