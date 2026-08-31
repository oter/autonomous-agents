# age multi-recipient, and whether the broker already exists

Type: research
Status: resolved
Blocked by:

## Question

[ADR-0001](../../../docs/adr/0001-runner-local-secret-broker.md) commits to a
Runner-local broker holding an age identity, with secrets encrypted to several
recipients. Establish that this is actually how `age` works, and find out
whether it needs building at all.

- Confirm multi-recipient encryption in `filippo.io/age`: encrypting one value
  to N recipients, decrypting with any one identity, and what the ciphertext
  costs in size when inlined into YAML.
- What is the Go API for loading an identity from a file and decrypting a value
  in memory? Any footguns around armored vs binary encoding in YAML?
- **Does this tool already exist?** Before we write a broker and a `dsecrets`,
  survey what covers the same ground: `sops` with age, `age-plugin-*`, `envchain`,
  `summon`, `chamber`, systemd credentials, Docker secrets. For each, say
  specifically why it does or does not give us "decrypt only these names, exec
  the child with them in its env, log the access".
- What is the minimum viable unix-socket broker in Go — how does it identify
  which Agent is calling, given the caller is in a container on the same host?
  That last part is the sharp bit and ticket 05 depends on the answer.

## Answer

Full findings: [`research/02-age-multi-recipient-and-broker.md`](../research/02-age-multi-recipient-and-broker.md).

**Build the Broker. Reuse nothing for it.** Roughly 150 lines. The survey was
closer than expected — a working `sops keyservice` alternative was stood up
before it failed — but every existing local-daemon secret tool brokers a *key*,
and we need to broker a *value*. That difference is the whole finding, and it is
structural rather than a missing feature.

- **`sops keyservice` is a blind decryption oracle.** Its wire request is
  `{Key, ciphertext}` — no secret name, no caller identity — so an unrelated
  Agent's secrets file was decrypted through it unchallenged. That is not
  theoretical here: Agent YAMLs live in a repository a Run holds a credential
  for. It also cannot express a name-level Allowlist, and its server-side audit
  log for two requests was `Decryption succeeded`, twice.
- **`sops exec-env` orphans its child on `SIGTERM`.** It forks and forwards no
  signals, which silently breaks the Teardown contract — a timed-out Run would
  leave no Journal at all. This one would not have been caught by reading docs.
- **age plugins cannot work either**, for the same structural reason: their
  interface is `Unwrap(stanzas) -> fileKey`, a key, not a named value.
- **Vault/OpenBao is the only genuine 4/4**, and is still rejected: a stateful
  secrets server per Runner reopens two charting decisions to save 150 lines.
- **`summon` is viable for the `dsecrets` half** — it does forward signals — but
  it pairs responses positionally and silently drops denied names. Fail-open,
  where ticket 05 wants fail-loud.

**Caller identification is a per-Run unix socket, in a per-Run directory,
bind-mounted into only that Run's container.** Identity is the listener that
accepted the connection: unforgeable, because no other Run's socket exists in
that container's filesystem. Revocation is `os.RemoveAll`.

`SO_PEERCRED` was tested and does work — the Broker in the host PID namespace
gets a real host PID, and `/proc/<pid>/cgroup` matched `docker inspect` exactly —
but uid is 0 for every container, the PID-to-`/proc` lookup is a reproducible
race, and it silently degrades to `pid=0` if the Broker is ever containerised.
Not worth a `/proc` parser in the security path.

**Two operational constraints that change the design:**

1. **Bind-mount the directory, never the socket file.** A file bind-mount pins
   the inode, so a Broker restart leaves every running container on
   `ECONNREFUSED` until it is recreated. Directory mounts survive a restart.
2. **A macOS host cannot bind-mount a unix socket into a container.** Measured:
   it is visible, it stats as a socket, and it refuses connections. **The Mac
   mini Runner therefore needs its Broker containerised, sharing a Docker volume
   with the Run** rather than a host path. This is a real divergence between the
   two Runners and it lands on tickets 05, 06 and 08.

**age is confirmed as assumed.** Multi-recipient encryption works; a 40-byte
token becomes 662 bytes / 12 lines armored at three recipients, matching the
spec's formula; the YAML footgun is the `>` folded scalar, not CRLF. Use **one
age file per Agent, not one per secret** — 2.7x smaller, 16 lines against 45 for
a realistic Agent.

**Nothing invalidates ADR-0001 or the map.** Two refinements folded back into
ADR-0001: the Broker must be *name-addressed* or its audit claim is vacuous, and
the known ceiling is sharper than written — a Run need not wrap a command to
capture a secret, since any same-uid sibling process can read
`/proc/<child>/environ`.

### Superseded in part

The "build the Broker" verdict above was **overtaken by a later decision**, not
by an error. [ADR-0002](../../../docs/adr/0002-per-run-key-envelope.md) removed
the Broker in favour of a per-Run key envelope, so the survey's conclusion no
longer applies to a component that exists.

The rest of this ticket's findings survive and are load-bearing: age
multi-recipient behaviour and ciphertext sizing, the YAML `>` folded-scalar
footgun, one age file per Agent rather than per secret, and — most importantly —
that **`sops exec-env` orphans its child on `SIGTERM`**, which is now a direct
constraint on `dsecrets` rather than on a Broker. The caller-identification
finding is moot: with a per-Run envelope there is no caller to identify.
