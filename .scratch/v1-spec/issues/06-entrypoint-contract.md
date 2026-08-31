# The container entrypoint and teardown contract

Type: grilling
Status: resolved
Blocked by: 01, 04, 05

## Question

Specify what happens inside a Run's container, in order, from `docker start` to
exit. This is the ticket that makes "the container pushes at teardown" real.

- The full sequence: install skills, fetch secrets, write the trigger payload,
  clone whatever the Run works on, start the agent process, capture its output,
  then teardown.
- **Where does each phase's failure go?** If skills fail to install, does the Run
  proceed without them, or abort? Charting gave skill installation its own
  timeout — say what happens when it expires.
- Teardown must fire on every exit path: clean exit, agent crash, and `SIGTERM`
  from a timeout kill. Specify the trap, what runs inside it, and its own time
  budget inside the 30-second `docker stop` grace period. What happens if
  teardown itself exceeds it.
- What does teardown push, in what order, and what happens if the Journal push
  succeeds but the work push fails, or the reverse?
- Which parts of this are baked into the base image and which are generated per
  Run by the control plane?
- What is the contract with the control plane — how does the control plane learn
  a Run's outcome, given the container may be on the remote Runner?

> **Constraints from ticket 03:** the entrypoint must run the agent as
> `agent & wait $!` — a shell trap does not fire while a foreground command is
> running, so without this the 30s grace buys nothing and teardown never runs.
> And a Run must enforce its own time limit internally: the control plane cannot
> kill what it cannot reach, and an OOM kill is `SIGKILL` regardless.

> **From ticket 01:** codex rollout files are **zstd-compressed when cold**, so a
> `*.jsonl` glob at Teardown silently misses them. Sandbox denials exit `0` and
> `error_seen` is sticky, so the outcome must be read from the event stream, not
> `$?`. A codex network partition retries forever with no timeout — the internal
> self-limit is mandatory, not defensive.

> **From ticket 04:** write the event stream to a **file**, not a pipe the
> control plane reads. `claude` drains stdout before exiting and waits up to 30s
> if the consumer is slow, which would eat the whole grace before the Journal
> push. Writing to a file also removes the need to hold a log stream open across
> SSH, which ticket 03 found has no liveness detection. `stop_grace` is 90s.
> Live UI logs become best-effort and losing them costs nothing.

> **From ticket 05:** the base image needs `age` and `jq`. The entrypoint sets
> `DSECRETS_ENVELOPE` and `DSECRETS_NAMES`, and mounts the per-Run identity at
> `/run/dsecrets/identity` (0400) on a **tmpfs** so it never reaches a layer or a
> volume. The identity must survive for the whole Run — the agent calls
> `dsecrets` throughout, not once at startup.

## Answer

**Note:** the question above says "30-second `docker stop` grace". Superseded by
ticket 04 — `stop_grace` is **90s**, because `claude` drains its stdout for up to
30 seconds on its own before exiting.

### The shape

Everything a Run needs that is not baked into the image now arrives over the
**Run API** — an authenticated channel back to the control plane, introduced
here and specified in [ticket 11](11-the-run-api.md). The container fetches its
payload rather than having it pushed in, which sidesteps the fact that **the
control plane cannot write files to a remote Runner** — it has only the Docker
API. `docker cp` would have worked; fetching is what makes the same mechanism
also carry liveness and outcome.

Both Runners sit on the same private network as the control plane, so this is a
plain private-network route with no tunnelling.

### The entrypoint

```sh
#!/bin/sh
set -eu
JOURNAL=/run/journal; mkdir -p "$JOURNAL" /workspace

api() { curl -fsS --max-time 10 -H "Authorization: Bearer $RUN_TOKEN" "$@"; }
report() { api --data "$(jq -cn --arg s "$1" --arg m "${2:-}" \
             '{status:$s,message:$m}')" "$CONTROL_PLANE_URL/run/status" || true; }
fail()   { echo "entrypoint: $*" >&2; report failed "$*"; exit 1; }

# Teardown fires on EVERY exit path. `trap ... EXIT` covers clean exit and crash;
# trapping TERM into an explicit `exit` is what routes a stop-kill through it.
teardown() {
  rc=$?; trap - EXIT
  kill "$HEARTBEAT" "$LIMIT" 2>/dev/null || true
  kill -TERM "$AGENT" 2>/dev/null || true

  # BOTH globs. codex compresses rollouts when cold and a *.jsonl glob drops them.
  find "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" \
       \( -name '*.jsonl' -o -name '*.jsonl.zst' \) \
       -exec cp {} "$JOURNAL/" \; 2>/dev/null || true

  push_work && w=pushed || w=failed        # work first (Q36)
  jq -n --arg rc "$rc" --arg w "$w" '{exit_code:$rc,work_push:$w}' \
     > "$JOURNAL/outcome.json"
  push_journal || true                     # journal last, so it records the above
  report finished "$rc"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 143' TERM INT

api "$CONTROL_PLANE_URL/run/payload" -o /run/trigger.json || fail "payload fetch"

[ -z "${AGENT_SKILLS:-}" ] || timeout "${SKILLS_TIMEOUT:-300}" \
  install_skills "$AGENT_SKILLS" || fail "skills install"

clone_repos "${AGENT_REPOS:-}" || fail "clone"

( while sleep 30; do report running; done ) & HEARTBEAT=$!
( sleep "$WALL_CLOCK_SECONDS"; kill -TERM 1 ) & LIMIT=$!

build_argv                                  # sets "$@" from AGENT_CLI et al.
"$@" >"$JOURNAL/stream.jsonl" 2>"$JOURNAL/stderr.log" </dev/null &
AGENT=$!
wait "$AGENT"
```

### Why each awkward line is there

- **`& wait $!`, never foreground.** A shell trap does not fire while a
  foreground command is running, so a foreground agent means the 90s grace buys
  nothing and Teardown never runs at all. (ticket 03)
- **`trap 'exit 143' TERM` on top of `trap teardown EXIT`.** The TERM handler's
  only job is to *cause an exit*, which is what routes a stop-kill through the
  single teardown path. One teardown implementation, every exit path.
- **stdout to a file, never a pipe.** `claude` waits up to 30s draining stdout if
  its consumer reads slowly, which would consume the whole grace before the
  Journal push. Writing to a file also removes any need to hold a log stream open
  across the Runner link. (tickets 01, 04)
- **`</dev/null`.** `codex` hangs if stdin is left open. (ticket 01)
- **The internal `sleep && kill -TERM 1`.** Mandatory, not defensive: a codex
  network partition retries **forever** with no timeout, and the control plane
  cannot kill what it cannot reach. (tickets 01, 03)
- **Both globs in the `find`.** codex compresses rollouts when cold; `*.jsonl`
  alone silently drops them. (ticket 01)

### Failure policy per phase

**Payload fetch fails → abort.** There is nothing to do without a trigger.

**Skills fail to install, or the install times out → abort.** An Agent that
declares skills has a prompt that assumes them, so running without is a silent
behaviour change — and ticket 01 already caught `claude --bare` producing exactly
that. Skill installation gets its own timeout and does not consume the Run's wall
clock. A flaky network killing a nightly Run is cheaper than a nightly Run that
quietly did the wrong thing.

**Clone fails → abort.** Same reasoning.

Every abort still runs Teardown, because the trap is installed before any of
them. A failed Run leaves a Journal explaining itself.

### Push order

**Work first, Journal last.** The Journal then records the work push's outcome in
`outcome.json`, which is the whole point of doing it in that order. This was the
opposite of the original recommendation — journal-first, on the grounds that a
lost Journal is unreconstructable — and the Run API is what changed it: with
heartbeats flowing throughout, the control plane already holds a partial record,
so the Journal push is no longer the single point of loss.

If Teardown overruns the 90s grace it is SIGKILLed and the Journal is lost, but
the control plane still has the heartbeat trail and the authoritative exit code.

### How the control plane learns the outcome

**Both ends, deliberately.** The container reports `running` every 30s and
`finished` with its exit code. The control plane also **polls
`ContainerInspect`** for `State.ExitCode` — ticket 03 warned that `ContainerWait`
is an unreliable narrator through a middlebox and to poll `Inspect` instead.

The two are not redundant. A container that is SIGKILLed, OOM-killed, or loses
power is structurally unable to report its own death; only `Inspect` sees that.
Equally, `Inspect` cannot see that a Run is wedged but alive; only the heartbeat
gap shows that.

**If the control plane is unreachable mid-Run, the Run continues.** It already
holds its payload and its repos, and Teardown pushes both without help.
Heartbeats fail silently and the control plane reconciles from `Inspect` when the
link returns. Aborting instead would mean a control-plane redeploy kills every
in-flight Run.

The exception, recorded honestly in ADR-0003: a command needing a **secret** will
fail while the link is down, because secrets now come from the control plane.

### Baked versus per Run

**Baked into the base image:** both CLIs, the `skills` CLI, `curl`, `jq`, `git`,
`dsecrets`, and this entrypoint. Note `age` is **not** needed in the image any
more — ADR-0003 moved decryption to the control plane.

**Per Run, all as environment:** `RUN_TOKEN`, `CONTROL_PLANE_URL`, `AGENT_CLI`,
`AGENT_PROMPT`, `AGENT_PERSONALITY`, `AGENT_EXTRA_ARGS`, `AGENT_SKILLS`,
`AGENT_REPOS`, `WALL_CLOCK_SECONDS`, `TRIGGER_KIND`, `TRIGGER_NAME`, and the
model credential. Nothing is bind-mounted and nothing is `docker cp`'d.

**`build_argv` lives in the entrypoint, not in Go.** The divergence list is long
and entirely CLI trivia — `codex` needs `</dev/null`,
`--dangerously-bypass-approvals-and-sandbox` inside Docker and
`--skip-git-repo-check`; `claude` must **not** get `--bare`; the stream flags and
the personality flags differ. All of it changes when the CLIs change, which is an
image rebuild either way. In Go it would mean a control-plane redeploy every time
a vendor renames a flag.

### Amended by ticket 11

**The agent runs unprivileged.** Ticket 11 restricts a Run's network egress to
the internet plus the control plane, blocking the rest of the tailnet and the
LAN, and enforces it with iptables **inside** the container so both Runners
behave identically. A root agent would simply `iptables -F` and the rules would
be decoration.

So the entrypoint gains a phase, before anything else:

```sh
# Container is created with --cap-add NET_ADMIN and a non-root `agent` user.
install_egress_rules          # allow internet + $CONTROL_PLANE_IP:$PORT,
                              # drop 100.64/10, 10/8, 172.16/12, 192.168/16
```

and the agent is launched through `setpriv --reuid agent --regid agent --clear-groups`
rather than directly, dropping `NET_ADMIN` with it.

Consequences to carry into the spec: the workspace, `CODEX_HOME`,
`CLAUDE_CONFIG_DIR` and the Journal directory must all be writable by `agent`;
skill installation and `git clone` run as `agent`, so anything they need must not
require root; and the teardown trap runs in the **root** entrypoint shell, which
is what allows it to push even if the agent has wedged its own user session.

The container needs `--cap-add NET_ADMIN` at create. That is the only capability
added, and it is dropped before the agent starts.

### Amended by ticket 07

**The Journal goes to object storage, not git.** The `agentruns` repository is
dropped, so `push_journal` in the sketch above is wrong. Teardown instead:

```sh
  push_work && w=pushed || w=failed        # still git — this is real code
  jq -n ... > "$JOURNAL/meta.json"
  tar --zstd -cf /run/run.tar.zst -C "$JOURNAL" .
  urls=$(api "$CONTROL_PLANE_URL/run/journal-urls")   # two presigned PUTs
  curl -fsS -T "$JOURNAL/meta.json"  "$(echo "$urls" | jq -r .meta)"
  curl -fsS -T /run/run.tar.zst      "$(echo "$urls" | jq -r .archive)"
```

The **work-first, Journal-last** order is unchanged and the reasoning is
unchanged: `meta.json` records whether the work push succeeded.

Teardown gets materially safer as a result. A git push needed a clone whose cost
grows with every Run ever recorded; two PUTs are flat forever, which matters
because this is the code most likely to be racing a `SIGKILL`.

No storage credential enters the container — the presigned URLs are minted when
Teardown starts, so a long Run cannot outlive them.
