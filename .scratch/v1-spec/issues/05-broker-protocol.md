# The `dsecrets` interface and the per-Run key envelope

Type: grilling
Status: resolved
Blocked by: 02

## Question

[ADR-0002](../../../docs/adr/0002-per-run-key-envelope.md) replaced the Broker
with a per-Run key envelope: the control plane re-encrypts an Agent's allowlisted
secrets to a fresh per-Run age identity, injects the ciphertext as environment
variables and the identity as a file, and `dsecrets` decrypts locally and `exec`s
the child. This ticket pins down that contract. It no longer specifies a daemon
or a wire protocol.

- **The command line.** Charting sketched `dsecrets --flags {what to decrypt
  into what} -- actual-cmd`. Settle the real shape: how a caller names secrets,
  how a secret name maps onto an environment variable name for the child, and
  whether decrypting to a **file** (an SSH key, a credentials JSON) is supported
  or deliberately not.
- **`exec`, not fork, and forward signals.** Ticket 02 measured `sops exec-env`
  orphaning its child on `SIGTERM`. The same bug here silently breaks Teardown
  and loses the Journal of every timed-out Run — so specify the process model
  explicitly and say how it is tested.
- **How the ciphertext is carried.** One environment variable per secret, or one
  envelope variable holding all of them? Ticket 02 found one age file per Agent
  is 2.7x smaller than one per secret; the same question applies here, and the
  answer interacts with how `env` output looks to a curious agent.
- **What happens on a name that has no ciphertext.** Under ADR-0002 the
  Allowlist is enforced by what ciphertext exists, so an unknown name is not a
  permission denial — it is a missing variable. Fail loudly regardless: ticket 02
  found `summon` silently drops denied names, which is the wrong default.
- **Where the identity file lives, and its lifecycle.** Path, mode, whether the
  entrypoint removes it after the agent process starts, and what that costs if a
  Run needs to decrypt something later in its life.
- **Failure modes**: identity file missing, ciphertext undecryptable by this
  Run's identity, malformed envelope. Each should produce an error a human can
  act on from the Journal alone.
- **Does `dsecrets` need to exist, given ADR-0002?** Ticket 02 ruled out `sops
  exec-env` on the signal bug and `summon` on fail-open. Confirm that verdict
  still holds now that there is no Broker and no runtime Allowlist check — the
  tool is much smaller than it was, and the bar for writing it should be re-met,
  not inherited.

## Answer

### Does it need to exist? Yes, but as a shell script, not a Go binary

The Go binary was recommended and **overruled in favour of shell**. The two
objections that motivated Go — trailing-newline corruption and failing open —
are both fixable in POSIX shell, so the recommendation does not survive its own
reasoning. Reference implementation:

```sh
#!/bin/sh
# dsecrets NAME[,NAME...] -- command [args...]
set -eu

names=${1:?usage: dsecrets NAME[,NAME...] -- cmd}
shift
[ "${1:-}" = "--" ] || { echo "dsecrets: expected -- before the command" >&2; exit 2; }
shift

# set -e aborts here if the identity is missing or the envelope will not decrypt.
plain=$(age -d -i /run/dsecrets/identity <<EOF
$DSECRETS_ENVELOPE
EOF
)

for n in $(echo "$names" | tr ',' ' '); do
  printf '%s' "$plain" | jq -e --arg n "$n" 'has($n)' >/dev/null || {
    echo "dsecrets: no such secret: $n (have: $DSECRETS_NAMES)" >&2
    exit 3
  }
  # -j suppresses jq's own trailing newline; the X sentinel stops $() from
  # eating a genuine one. Together they keep the value byte-exact.
  v=$(printf '%s' "$plain" | jq -j --arg n "$n" '.[$n]'; printf X)
  export "$n=${v%X}"
done

exec "$@"
```

Three details that are load-bearing rather than stylistic:

- **`has($n)` is checked before the value is read.** The obvious form,
  `v=$(jq -er ... ; printf X) || fail`, is broken: the exit status of a command
  substitution is the status of its *last* command, which is the `printf`, so
  the `||` never fires. Two jq calls, correct and obvious, beat one clever one.
- **`jq -j` plus the `X` sentinel** is what makes values byte-exact. `-j`
  suppresses the newline jq would add; the sentinel survives `$()` stripping a
  newline the value genuinely ends with.
- **`exec` replaces the process.** Signals reach the child directly because
  nothing is left in the way — the `sops exec-env` orphaning bug from ticket 02
  is structurally impossible here rather than handled.

Both `age` and `jq` therefore have to be in the base image.

`ponytail:` shell, not Go. Move to a Go binary if the error messages need to be
richer than `echo`, or if a future secret sink needs something `$()` cannot carry.

### The envelope

**One `DSECRETS_ENVELOPE`** — a single age ciphertext whose plaintext is a flat
JSON object of every secret the Run is allowed. One decrypt per invocation, and
2.7x smaller than one ciphertext per secret (ticket 02's measurement).

Alongside it, **`DSECRETS_NAMES` in plaintext**, a comma-separated list of the
keys. It costs nothing, lets an unknown-name error say what *is* available, and
means the agent can discover what it holds without the YAML author having to
recite the list in the prompt by hand.

JSON rather than `KEY=VALUE` lines so that values with newlines or `=` in them
need no escaping rules.

### The command line

`dsecrets NAME[,NAME...] -- command [args...]`. The secret name **is** the
environment variable name; there is no renaming, because the YAML author already
chose it.

**No file sink.** It was in the charting sketch for things like SSH keys, but the
one git credential is a fine-grained PAT, which lives in an environment variable
perfectly well. Dropped: no path handling, no mode juggling, no cleanup. It comes
back the day something genuinely needs a file on disk.

**No "decrypt everything" mode.** Naming what a command needs is the entire
discipline, and a blanket flag defeats it at zero friction.

### The identity

`/run/dsecrets/identity`, mode 0400, on a **tmpfs** so it never reaches a layer
or a volume. **Kept for the Run's whole life** — it cannot be removed after
startup, because the agent invokes `dsecrets` throughout its work, not once.

### Failure modes

Every one exits non-zero **without running the child**, with the reason on
stderr so the Journal alone explains it: exit 2 for a malformed invocation, exit
3 for an unknown name (listing what is available), and `set -e` aborting for a
missing identity file or an envelope that will not decrypt.

### The accepted hole

**The model credential is plaintext in the agent's own environment.**
`ANTHROPIC_API_KEY` / `CODEX_API_KEY` has to be, because the CLI itself needs it,
and wrapping the CLI in `dsecrets` changes nothing — the CLI *is* the agent, so
`/proc/self/environ` has it either way.

So the honest claim is narrower than "plaintext never enters the agent's
context": **the agent can always read the model credential it is currently
using; everything else stays ciphertext.** Accepted deliberately — the
alternative is an auth-injecting proxy, a whole component for one key whose blast
radius is spend the agent is already making. Recorded in ADR-0002.

### One runnable check

```sh
dsecrets FOO -- sh -c 'trap "echo caught; exit 0" TERM; sleep 30 & wait'
```

SIGTERM it, assert `caught` appears. That is the bug that silently loses the
Journal of every timed-out Run, so it is the one that gets a test.

### Amended by ticket 06

[ADR-0003](../../../docs/adr/0003-secrets-over-the-run-api.md) moved secrets onto
the Run API. The **mechanism** above is superseded: there is no envelope, no
per-Run age identity, and no `age` in the base image. Everything else — the
command-line shape, the process model, and the failure discipline — survives
unchanged.

```sh
#!/bin/sh
# dsecrets NAME[,NAME...] -- command [args...]
set -eu

names=${1:?usage: dsecrets NAME[,NAME...] -- cmd}
shift
[ "${1:-}" = "--" ] || { echo "dsecrets: expected -- before the command" >&2; exit 2; }
shift

# curl -f makes a non-2xx an error, so set -e aborts before the child runs.
# A denied name is a 403 from the control plane, not a silent omission.
resp=$(curl -fsS --max-time 10 \
  -H "Authorization: Bearer $RUN_TOKEN" -H 'Content-Type: application/json' \
  --data "$(jq -cn --arg n "$names" '{names: ($n | split(","))}')" \
  "$CONTROL_PLANE_URL/run/secrets")

for n in $(echo "$names" | tr ',' ' '); do
  printf '%s' "$resp" | jq -e --arg n "$n" 'has($n)' >/dev/null || {
    echo "dsecrets: control plane did not return: $n" >&2; exit 3; }
  v=$(printf '%s' "$resp" | jq -j --arg n "$n" '.[$n]'; printf X)
  export "$n=${v%X}"
done

exec "$@"
```

What survives from the original answer: `has($n)` checked before the value is
read, because a command substitution's status is its *last* command; `jq -j` plus
the `X` sentinel keeping values byte-exact; `exec` making the `sops exec-env`
orphaning bug structurally impossible; no file sink; no decrypt-everything mode;
the secret name being the environment variable name; and every failure exiting
non-zero without running the child.

What changes: the base image needs **`curl` and `jq`, and no longer needs
`age`**. The Allowlist is enforced **server-side** by the control plane rather
than by which ciphertext happens to exist. Every access is logged. And a secret
now requires the control plane to be reachable — see ADR-0003's consequences.
