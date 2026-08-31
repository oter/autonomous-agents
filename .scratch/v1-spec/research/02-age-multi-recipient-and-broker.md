# age multi-recipient, and whether the Broker already exists

Research for [ticket 02](../issues/02-age-multi-recipient-and-broker.md).
Date: 2026-08-31.

Claims marked **measured** were run on this machine against the real software.
Claims marked **source** were read in the shipped source or an official spec.
Everything else is cited inline. Versions are in
[Facts checked](#facts-checked-and-their-versions).

## Summary

**Verdict: build the Broker daemon. Reuse nothing for it.** It is roughly 150
lines. This is closer than expected — one candidate got most of the way — but
it fails, and it fails structurally rather than on a missing feature.

Every existing local-daemon secret tool brokers a **key**. We need to broker a
**value**. That one-word difference is the finding.

- **`sops` is the near miss, and it is very near.** It has a real daemon —
  `sops keyservice`, over a unix socket, holding the age identity — and
  `sops exec-env` really does exec a child with values in the child's
  environment. On paper that is our Broker plus our `dsecrets`, already written.
  I got it working end to end, then broke it two ways. **Measured: a caller
  pointed at a keyservice decrypted a completely unrelated Agent's secrets file
  through it, unchallenged**, because the keyservice's only verb is "unwrap this
  data key" and its request carries neither a secret name nor a caller identity
  (**source**: `DecryptRequest{Key, ciphertext}`). And **measured: `sops
  exec-env` forks rather than execs, and does not forward signals — SIGTERM to
  the wrapper orphaned the child**, which silently breaks the Teardown contract
  in `map.md`.
- **The same flaw sinks the `age` plugin protocol**, for the same reason.
  **Source**: a plugin's interface is `Unwrap(stanzas) -> fileKey`, and plugins
  are spawned as subprocesses. Once a plugin returns the file key the caller
  decrypts everything. A hypothetical `age-plugin-broker` could not enforce an
  Allowlist either.
- **The only genuine 4/4 is Vault / OpenBao**, and it costs a secrets server per
  Runner. See [the survey](#the-survey) — it is a real option and I am not
  hiding it, but it contradicts a charting decision and buys ops weight far
  exceeding the 150 lines it saves.

So the thing that must exist is small and specific: a daemon whose request is
*a list of names*, not *a blob of ciphertext*. **The Broker must own the
ciphertext and the Allowlist and accept only names.** If `dsecrets` ever sends
ciphertext to the Broker, the Allowlist means nothing and we have rebuilt the
sops keyservice. That is the single most important constraint to carry into
ticket 05.

The parts we are *not* building are the hard parts: `filippo.io/age` does
multi-recipient exactly as ADR-0001 assumes, and the sums work out.

**On caller identification** — full reasoning in
[§4](#4-how-the-broker-identifies-the-caller-the-sharp-one):
**give each Run its own socket inside its own directory, and bind-mount that
directory into only that Run's container.** Identity is then the listener the
connection arrived on. Nothing in the container can forge it, because no other
Run's socket exists in its filesystem. `SO_PEERCRED` does work here — that
surprised me, so I measured it both ways — but it buys a PID-reuse race and a
`/proc` parser for nothing the socket path does not already give us.

### What this does not change

Nothing invalidates ADR-0001 or `map.md`. Multi-recipient age is confirmed,
Runner-local key material is confirmed workable, and inline ciphertext in Agent
YAML round-trips. Three refinements, all additive, are in
[§6](#6-consequences-for-adr-0001-and-mapmd).

## The survey

Scored against the four things we actually need:

- **(a) Allowlist** — the caller asks for names and a policy *outside the
  caller's control* restricts what it gets. A manifest the caller supplies is
  not an allowlist.
- **(b) Child env only** — execs a child with the values in the child's
  environment, never the caller's.
- **(c) Audit** — a log of every access, where the caller cannot edit it.
- **(d) Local daemon** — key material held by a separate local process, not by
  the caller.

I added a fifth column, because `map.md`'s Teardown contract makes it
load-bearing and it eliminated the front-runner:

- **(e) Signals** — a SIGTERM to the wrapper reaches the agent process, so
  Teardown fires.

| Tool | (a) | (b) | (c) | (d) | (e) | Verdict |
|---|---|---|---|---|---|---|
| **sops + age**, default | NO | YES | weak | **NO** | **NO** | key in caller |
| **sops + `keyservice`** | **NO** | YES | **NO** | YES | **NO** | decryption oracle; orphans child |
| **age plugins** (`-yubikey`/`-tpm`/`-se`) | **NO** (structural) | n/a | NO | YES | n/a | brokers the file key |
| **summon** (cyberark) | NO | YES | NO | NO | **YES** | good wrapper, no policy |
| **chamber** | partial (IAM prefix) | YES | YES (CloudTrail) | NO | YES | needs AWS |
| **envchain** | NO | YES | NO | YES | YES | desktop keyring, not container |
| **aws-vault** | NO (profile) | YES | NO | partial | YES | AWS creds, not secrets |
| **teller** | NO | YES | NO | NO | ? | stale since 2024 |
| **direnv + sops** | NO | **NO** (parent shell) | NO | NO | n/a | inverted by design |
| **systemd credentials** | **YES** | partial (files) | NO | YES | n/a | **not available to Docker** |
| **Docker secrets** | partial | partial (files) | NO | NO | n/a | **swarm only** |
| **Compose `secrets:`** | NO | partial (files) | NO | NO | n/a | a bind mount |
| **BuildKit `--mount=type=secret`** | NO | build-time | NO | NO | n/a | wrong phase |
| **Vault Agent** (`env_template`+`exec`) | **YES** | **YES** | **YES** | partial (token) | YES | **4/4-ish, needs a server** |
| **OpenBao** (`bao agent`) | **YES** | **YES** | **YES** | **YES** | YES | **the only clean 4/4** |
| **Infisical** | YES (paid tier) | YES | partial | NO | ? | mandatory network service |
| **1Password `op run`** (headless) | partial (vault) | YES | YES | **NO** (token in env) | YES | proprietary, paid |
| **gopass `env --exec`** | partial (gpg) | YES | NO | YES (gpg-agent) | YES | no policy, no audit |
| **SPIFFE / SPIRE** | **YES** (attested) | **NO** | partial | YES | n/a | issues SVIDs, not secrets |
| **Podman `--secret type=env`** | NO | YES | NO | via `shell` driver | n/a | wrong container runtime |
| **envconsul** | NO — its `allowlist` filters the *parent env* | YES | NO | NO | ? | naming trap |
| **Keywhiz / Confidant / Knox** | — | — | — | — | — | archived / abandoned |
| **pass / git-crypt / ejson / sealed-secrets** | — | — | — | — | — | wrong shape entirely |

**Nothing is 4/4 without running a secrets server.** Three results are worth
the prose.

### sops is the near miss, and I pushed it as far as it goes

Worth taking seriously because it is MPL-2.0, CNCF, a single static binary, and
already installed on this machine. I built the whole arrangement.

`sops keyservice --network unix --address /tmp/sops.sock` is a genuine broker
daemon: **measured**, with the age key present only in the keyservice's
environment and the client run with `HOME` pointed at nothing,
`sops exec-env --enable-local-keyservice=false --keyservice unix://...`
decrypted successfully, and the keyservice log showed `[AGE] Decryption
succeeded`. Remove the keyservice and the client fails. So **(b) and (d) are
real**.

Then it breaks, three times.

**It is a decryption oracle.** I encrypted a second, unrelated Agent's secrets
to the same recipient and pointed the same caller at the same keyservice:

```
=== can a caller decrypt an arbitrary OTHER file through the same keyservice? ===
STOLEN: sk_live_this_belongs_to_a_different_agent
```

The keyservice cannot refuse, because its `DecryptRequest` carries only
`{Key, ciphertext}` — no names, no caller (**source**:
[keyservice.proto](https://github.com/getsops/sops/blob/main/keyservice/keyservice.proto)).
This matters concretely for us: every Agent YAML lives in a git repository the
Run holds a credential for, so a Run can fetch another Agent's ciphertext and
hand it to its own Broker.

**Its audit log is useless.** The entire server-side record of both requests:

```
[AGE] level=info msg="Decryption succeeded"
[AGE] level=info msg="Decryption succeeded"
```

No caller, no file, no names. sops *does* have a real append-only PostgreSQL
audit device with a deliberately non-configurable path — but it fires
**client-side**, so a Run that speaks the keyservice gRPC directly skips it
entirely.

**It orphans the child.** This is the one that ends it. **Measured** — `sops
exec-env` forks, stays resident as parent, and forwards nothing:

```
sops wrapper pid=26597 ; child: 26599 26597 /bin/sh
kill -TERM 26597
--- output ---
child pid=26599 started
!! child STILL RUNNING after SIGTERM to wrapper (orphaned)
```

`map.md` says the entrypoint wrapper traps SIGTERM and pushes the Journal, and
that "a Run that skipped teardown left no trace". With `sops exec-env` in the
process tree, `docker stop` kills sops and the agent keeps running until SIGKILL,
so Teardown never fires. That is a silent, total loss of the Run's record.

**The one arrangement that does fix (a) — and why it still is not worth it.**
Give each Agent its own age identity and run one keyservice per Agent holding
only that identity. **Measured**, this genuinely works:

```
=== A's socket, A's file (should work) ===   GOT: a-only
=== A's socket, B's file (MUST FAIL)   ===   ...at least one key has to be successful, but none were.
=== B's socket, B's file (should work) ===   AGENT_B_SECRET=b-only
```

Cryptographic isolation at Agent granularity, which *is* the Allowlist
granularity in `CONTEXT.md`. And **measured**, an Agent YAML can keep plaintext
config next to inline ciphertext using `encrypted_regex`, and stay hand-editable
in git if `mac_only_encrypted: true` is set in `.sops.yaml` — without it,
editing the plaintext `prompt:` field gives `MAC mismatch` and the file is dead.

But the price is one `sops keyservice` **process per live Run** (a single shared
keyservice is an oracle again, so per-Run is mandatory), per-Agent key
distribution, an audit log that records nothing usable, and the orphaned-child
defect, which alone is disqualifying. Our own Broker is one process per *Runner*
and does not have any of these problems.

### Vault / OpenBao is the only clean 4/4 — and I still would not

OpenBao's `bao agent` process supervisor mode does `env_template` + `exec` with
`Env: append(os.Environ(), newEnvVars...)`, its path-scoped policies are
deny-by-default and immutable to the caller, and audit devices declared in the
root-owned server config cannot be disabled through the API. That is genuinely
all four, MPL-2.0, single binary.

Against it: it means running a stateful secrets server on each of two Runners,
with unseal keys, backups, and an upgrade cadence — and `map.md` already decided
at charting for "`filippo.io/age` over a key-management service" and "no cloud
KMS", with ADR-0001 explicitly weighing and rejecting a central authority
because a Run on the remote Runner must survive the control plane being
unreachable. Adopting OpenBao reopens both. **The honest framing: it is not that
OpenBao cannot do this, it is that it is a much larger thing than the 150 lines
it replaces.** If the team ever wants cross-host identity or short-lived
rotating credentials, revisit it. (Confidence note: the OpenBao specifics came
via a sub-agent that flagged them as second-hand. Verify before relying on them.)

### summon is a real option for the `dsecrets` half

MIT, actively maintained (v0.11.0, 2026-03), pure Go, three dependencies. Its
provider protocol is a subprocess that reads secret paths on stdin and writes
base64 values on stdout — so a ~20-line provider could talk to our Broker, and
summon would be `dsecrets`. **Source**: it sets `runner.Env = env` and never
`os.Setenv`s a value, so **(b)** is real; and unlike sops it does
`signal.Notify` + `runner.Process.Signal(sig)`, so **(e)** is fine.

I still recommend our own ~40-line `dsecrets`, on two grounds. First, summon
pairs stream-mode responses to requests **by position**, and a provider that
omits a denied secret causes the variable to be silently absent rather than an
error — a fail-open shape in exactly the place ticket 05 wants "fail loudly".
Second, `syscall.Exec` is strictly better than signal forwarding for our case:
**measured**, exec-replacement keeps the agent at PID 1 so `docker stop` reaches
it directly:

```
wrapper is pid 1
agent now running as pid 1 (same pid = exec replacement)
AGENT GOT SIGTERM at pid 1 -> teardown runs
exit code: 0
```

Forty lines with no dependency and no fail-open edge beats a dependency plus a
provider plus four documented gotchas.

### Briefly, the rest

**systemd credentials** are the best-designed thing here and unavailable to us:
delivery is files in unswappable tmpfs, the unit file is root-owned so the
service cannot ask for anything, and `LoadCredential=ID:/path/to/socket` even
specifies a broker handshake where PID 1 authenticates the requesting unit via
`getpeername`. But a Docker container is not a unit on the host, and Docker
implements none of the container credential interface. Worth reading before
finalising ticket 05's protocol; not adoptable.

**Docker secrets** are swarm-only, stated plainly in Docker's own docs.
Compose's non-swarm `secrets:` is a bind mount with no daemon, policy, or audit.
BuildKit's `--mount=type=secret` is build-time only.

**chamber**, **aws-vault**, **gopass**, **1Password**, **envchain** all get (b)
right and most get (d), but none has a name-level allowlist enforced against the
caller, and each drags in a platform we do not have (AWS, a desktop keyring, a
paid SaaS). **chamber's `--strict`** is worth stealing as an idea regardless: the
caller sets `DB_PASSWORD=chamberme` to declare intent and the run fails closed if
it is not filled.

**SPIRE** solves our identification problem in production and is the reference
for it (see §4), but the Workload API spec forbids using it for arbitrary
secrets — it issues SVIDs, not values.

**envconsul** is a trap for the unwary: its config key `allowlist` filters the
*inherited parent environment*, not secrets. If anyone cites it as satisfying
(a), it does not.

## 1. Multi-recipient encryption in `filippo.io/age`

Confirmed. Multi-recipient is the default shape of the API, not an add-on.
`Encrypt` is variadic over recipients, `Decrypt` over identities, and the doc
comments state exactly the semantics ADR-0001 assumes:

> `Encrypt` encrypts a file to one or more recipients. Every recipient will be
> able to decrypt the file.

> `Decrypt` decrypts a file encrypted to one or more identities. All identities
> will be tried until one successfully decrypts the file.

Verified signatures (**source**, `filippo.io/age v1.3.2`):

```go
func Encrypt(dst io.Writer, recipients ...Recipient) (io.WriteCloser, error)
func Decrypt(src io.Reader, identities ...Identity) (io.Reader, error)
func GenerateX25519Identity() (*X25519Identity, error)
func ParseX25519Recipient(s string) (*X25519Recipient, error)
func ParseX25519Identity(s string) (*X25519Identity, error)
func ParseIdentities(f io.Reader) ([]Identity, error)
func (i *X25519Identity) Recipient() *X25519Recipient
// filippo.io/age/armor
func NewWriter(dst io.Writer) io.WriteCloser
func NewReader(r io.Reader) io.Reader
```

### Encrypting one value to N recipients

This compiles and ran; it produced the measurements below.

```go
import (
    "bytes"
    "io"

    "filippo.io/age"
    "filippo.io/age/armor"
)

// recipientStrings are the "age1..." strings from config: the control plane,
// plus whichever Runners may read this secret.
func encryptTo(plain []byte, recipientStrings []string) ([]byte, error) {
    var recipients []age.Recipient
    for _, s := range recipientStrings {
        r, err := age.ParseX25519Recipient(s)
        if err != nil {
            return nil, err
        }
        recipients = append(recipients, r)
    }

    out := &bytes.Buffer{}
    armorWriter := armor.NewWriter(out)
    w, err := age.Encrypt(armorWriter, recipients...)
    if err != nil {
        return nil, err
    }
    if _, err := w.Write(plain); err != nil {
        return nil, err
    }
    // Both Closes are required, in this order. Closing only the outer writer
    // silently truncates the payload.
    if err := w.Close(); err != nil {
        return nil, err
    }
    if err := armorWriter.Close(); err != nil {
        return nil, err
    }
    return out.Bytes(), nil
}
```

### Loading an identity from a file, and decrypting with any one of them

`age.ParseIdentities` reads the file `age-keygen` writes as-is — **measured**, it
skips the `#` comment lines `age-keygen` puts at the top, so no preprocessing is
needed. This is the Broker's startup path.

```go
func loadIdentities(path string) ([]age.Identity, error) {
    f, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer f.Close()
    return age.ParseIdentities(f) // skips "#" comments and blank lines
}

func decrypt(ciphertext []byte, identities []age.Identity) ([]byte, error) {
    r, err := age.Decrypt(armor.NewReader(bytes.NewReader(ciphertext)), identities...)
    if err != nil {
        return nil, err // see NoIdentityMatchError below
    }
    return io.ReadAll(r)
}
```

**Measured**, encrypting one value to three recipients (control plane,
`runner-local`, `runner-macmini`) then decrypting with each identity
*individually*:

```
control-plane    -> true  err=<nil>
runner-local     -> true  err=<nil>
runner-macmini   -> true  err=<nil>
non-recipient    -> err=identity did not match any of the recipients:
                        incorrect identity for recipient block
```

The failure is typed, which lets the Broker distinguish "this Runner is not a
recipient of this secret" — an operator provisioning error, and one of ticket
05's named failure modes — from a corrupt blob:

```go
var noMatch *age.NoIdentityMatchError
if errors.As(err, &noMatch) {
    // this Runner was not among the recipients: a provisioning error,
    // not a corrupt ciphertext. Say so in the error the human reads.
}
```

Also **measured**: a value encrypted by the **`age` CLI** (`age -a -r age1...`)
decrypts with the Go library. Operators can use the CLI, the control plane can
use the library, and there is no format drift between them.

## 2. Ciphertext size when armored and inlined into YAML

**A 40-byte token encrypted to 3 recipients becomes 662 bytes of armor across 12
lines — about 16.5x.** To 2 recipients, 528 bytes across 10 lines.

Overhead is dominated by a fixed per-recipient cost, not by the payload.
**Measured**, 40-byte plaintext:

| Recipients | Binary | Armored | Armor lines |
|---|---|---|---|
| 1 | 240 B | 393 B | 7 |
| 2 | 338 B | 528 B | 10 |
| 3 | 436 B | 662 B | 12 |
| 4 | 534 B | 792 B | 14 |

Measurements match the [spec](https://github.com/C2SP/C2SP/blob/main/age.md)
exactly, which is good evidence both are right:

```
binary = 22        (version line "age-encryption.org/v1\n")
       + 98 * N    (per X25519 stanza: "-> X25519 " + 43 b64 + \n, then 43 b64 + \n)
       + 48        (MAC line: "--- " + 43 b64 + \n)
       + 16        (payload nonce)
       + 16        (ChaCha20-Poly1305 tag, one per 64 KiB chunk)
       + len(plaintext)
```

N=3, plaintext=40: `22 + 294 + 48 + 16 + 16 + 40 = 436`. Measured 436. Armor is
base64 at 64 columns plus two PEM lines: `4*ceil(436/3) = 584` chars over 10
lines, `584 + 10 + 35 + 33 = 662`. Measured 662.

**The per-recipient cost is paid once per age file**, which is the number that
should drive the schema. **Measured** on a realistic Agent — 4 secrets (GitHub
PAT, Linear key, Anthropic key, Slack webhook), 253 bytes of plaintext, 2
recipients:

| Strategy | YAML bytes | YAML lines |
|---|---|---|
| One age file **per secret** | 2643 | 45 |
| One age file **per Agent** (dotenv inside) | 964 | 16 |

**Recommend one age file per Agent, not one per secret.** 2.7x smaller, 16 lines
instead of 45, and adding a fifth secret then costs ~30 bytes rather than another
660 — so the file does not grow a screenful per secret. Since `map.md` pins each
Agent to one Runner, the recipient set is identical for every secret in an Agent
anyway (`{control plane, that Agent's Runner}`), so per-secret recipient sets buy
nothing today.

Per-name enforcement is unaffected: the Broker decrypts the one blob and returns
only Allowlisted names, which is where ADR-0001 already puts the check.

For reference, one Agent's entire secret set inlined:

```yaml
secrets: |
    -----BEGIN AGE ENCRYPTED FILE-----
    YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+IFgyNTUxOSBOSWxTYkpQYlNqSWp0eU10
    ... 11 more lines ...
    -----END AGE ENCRYPTED FILE-----
```

`sops` inlines an armored age block into YAML exactly this way for its own data
key, so the approach is not exotic.

## 3. Armored vs binary in YAML — the footguns

**Binary is not an option.** **Measured**: age binary ciphertext is not valid
UTF-8, so it cannot enter a YAML file without an encoding step. The real choice
is armor (662 B, 12 lines) versus base64-of-binary on one line (584 B, 1 line).
Armor is 13% larger, self-describing, and what every other age tool emits — **use
armor**.

**Measured** behaviours:

| Case | Result |
|---|---|
| `\|` literal block scalar, hand-written | works |
| `\|-` literal block, trailing newline stripped | works |
| `yaml.Marshal` of the armored string | picks `\|`/`\|-` automatically; byte-identical round trip; decrypts |
| Blank lines before/after the armor block | works |
| CRLF line endings inside the armor | **works** — Go's armor reader tolerates CRLF |
| `>` folded block scalar | **FAILS** |
| Leading whitespace on armor lines | **FAILS** |
| Trailing whitespace on armor lines | **FAILS** |
| Binary ciphertext pasted raw | **FAILS** — not valid UTF-8 |

**1. The folded `>` scalar destroys the ciphertext.** This is the one that will
bite, because `>` and `|` look interchangeable and a reformatter may swap them.
YAML folds lines together with spaces:

```
failed to read header: parsing age header: failed to read intro:
invalid armor: invalid first line:
"-----BEGIN AGE ENCRYPTED FILE----- YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+..."
```

**Ticket 04's schema validator should reject a secrets value whose first line is
not exactly `-----BEGIN AGE ENCRYPTED FILE-----`**, so this is caught at load
time rather than mid-Run.

**2. Whitespace is fatal, and block scalars are why it usually is not.** Go's
armor reader is documented "strict" and means it — one leading or trailing space
on any line fails the parse. A `|` block scalar strips block indentation on the
way out, which is why hand-written YAML works. Blank lines *around* the block are
fine. Since base64 contains no spaces, strictness is harmless unless something
rewrites the file.

**3. CRLF is fine.** The spec says implementations *may* accept CRLF and the Go
one does, so `core.autocrlf` will not silently break the corpus.

Two smaller notes: reading armored input without `armor.NewReader` (or binary
input with it) produces a clear, distinguishable error, so a mis-encoded blob
fails legibly; and a trailing newline is optional, so the schema need not care
whether the author used `|` or `|-`.

## 4. How the Broker identifies the caller (the sharp one)

**Recommendation: a per-Run socket inside a per-Run directory, bind-mounted into
only that Run's container. Not `SO_PEERCRED`. Not a bearer token.**

The threat is narrow, and stating it rules out most of the design space: a Run
must not obtain *another Agent's* secrets. A Run reading its own secrets is the
feature. So the Broker needs no rich notion of a principal — only an unforgeable
answer to "which Run is on the other end of this connection".

### Why not `SO_PEERCRED`

Not because it fails. **It works**, which surprised me, so I measured both
arrangements on real Linux.

With the Broker in the **host** PID namespace — the ADR-0001 arrangement —
`SO_PEERCRED` returns a real host-side PID, and `/proc/<pid>/cgroup` yields the
container id:

```
CONN 1 pid=1396277   cgroup: 0::/../23df8d82d4998ed8c6997e9ef2a9595b1bd610e858819d0329a3a91873fb7214
docker inspect id:            23df8d82d4998ed8c6997e9ef2a9595b1bd610e858819d0329a3a91873fb7214
```

An exact match. This is sound because the broker reads `/proc` from the host's
initial cgroup namespace, so it sees the absolute path
(`cgroup_namespaces(7)`), and because a process is visible in every ancestor PID
namespace (`pid_namespaces(7)`), with `pid_vnr()` rendering the PID in the
*reader's* namespace (kernel `kernel/pid.c`).

Three things kill it anyway.

**The uid carries no information.** **Measured** across three containers: two
running as root both reported `uid=0`. Docker does not enable `userns-remap` by
default, and even when enabled it uses one shared range for the whole daemon, so
every container's root maps to the same host uid either way. Only the PID is
informative.

**The PID is a race.** The Broker learns a PID, then must read `/proc/<pid>` to
resolve it — and the process can be gone. **Measured**, with a client that exits
the instant it connects and a Broker that takes 1.5s to look up identity:

```
CONN 2 pid=1396352 (exits-immediately)
   IDENTITY LOOKUP FAILED: [Errno 2] No such file or directory: '/proc/1396352/cgroup'
```

That is the benign version. The malicious version is PID reuse between the
`SO_PEERCRED` read and the `/proc` read, attributing the request to the wrong
Agent. This is a real CVE-class bug, not a theoretical one: SPIRE shipped a fix
for exactly it, and its own code says peer credentials "SHOULD NOT be used" for
authorization without the mitigation. Doing it right means SPIRE's full
recipe — open `/proc/<pid>` *first*, snapshot `starttime` from
`/proc/<pid>/stat`, attest, then re-verify liveness and uid/gid *after* use —
plus a cgroup parser correct across v1/v2 × cgroupfs/systemd × nesting.
`SO_PEERPIDFD` closes the race cleanly but needs Linux ≥ 6.5 on both Runners and
is not in man-pages at all.

**It returns zero identity, silently, if the Broker is ever containerised.**
**Measured**, Broker in its own PID namespace, client in another:

```
CONN 1: SO_PEERCRED pid=0 uid=0 gid=0 | client says: my pid in MY ns=1
```

**`pid=0`** — sibling namespaces cannot translate, and the kernel reports 0
rather than an error. A security boundary that silently degrades to "no
identity" is the wrong shape.

Also: the cgroup path format is not portable. **Measured** `0::/../<id>` here;
it is `/system.slice/docker-<id>.scope` under the systemd cgroup driver and
`/docker/<id>` under cgroupfs. And none of it can be compiled or tested on a
macOS dev machine — `unix.Ucred` and `GetsockoptUcred` are Linux-only, so this
would be the most security-critical and least testable code in the system.
(macOS has `LOCAL_PEERCRED`/`Xucred`, which has **no pid field** at all.)

### Why not a per-Run bearer token

The client reading its own token is fine — it *is* that Run. The problem is
everything around it. A token must live somewhere in the container; as an env
var it is exposed by `docker inspect` (`Config.Env`), so **anything with
`/var/run/docker.sock` reads every Run's token**, and agent containers are
exactly the kind of workload that tends to get docker.sock mounted. It is also
readable via `/proc/<pid>/environ` by any same-uid process — **measured**:

```
/proc/6/environ perms: -r-------- root:root
grep for the secret: SECRET_TOKEN=ghp_supersecret     # same uid: readable
su low -c "... /proc/6/environ"  -> Permission denied  # different uid: blocked
```

And it requires the Broker to keep a token table, expire it at Teardown, and
handle replay — real state and real lifecycle bugs — to arrive where the socket
path gets us with none of it. A token is right when the client can reach the
server from anywhere. Ours cannot, and we control the filesystem it sees.

### Why the per-Run socket

Identity becomes a property of the *channel*, established by the control plane at
spawn, unforgeable from inside the container in the strongest available sense:
**no other Run's socket exists anywhere in that container's filesystem.** Nothing
to guess, steal, replay, or leak into a log. The Broker does no parsing, keeps no
token table, and needs no kernel features or platform-specific code.

It also composes with the ephemeral-Run model in `map.md`: the directory is
created with the Run and removed at Teardown, so **revocation is `os.RemoveAll`**
— atomic, total, and it survives Broker bugs.

```
host:      /run/agentbroker/<run-id>/broker.sock
container: /run/agent/broker.sock          (bind mount of the DIRECTORY, ro)
```

**One critical operational detail, measured.** Bind-mount the *directory*, never
the socket file. A file bind-mount pins the inode, so a Broker restart leaves
every running container pointing at a dead one:

```
=== A. BIND-MOUNT THE SOCKET FILE ===
connect via /dst/s.sock : OK -> listener-v1
-- broker restarts: unlink + recreate --
connect via /src/s.sock : OK -> listener-v2
connect via /dst/s.sock : FAIL -> ConnectionRefusedError: [Errno 111]   <-- the bind mount

=== B. BIND-MOUNT THE CONTAINING DIRECTORY ===
connect via /dst2/s.sock: OK -> listener-v1
-- broker restarts: unlink + recreate --
connect via /dst2/s.sock: OK -> listener-v2   <-- survives
```

With a file mount, a Broker restart silently breaks every in-flight Run and the
only fix is recreating containers. With a directory mount, the Broker recreates
its socket and running containers reconnect.

Three supporting details:

- **The socket must exist before `docker run`.** runc errors out if a bind-mount
  source is missing, so the Broker must `net.Listen` before the container starts.
  That is the natural ordering anyway.
- **Use `--mount type=bind,...`, not `-v`.** Docker's docs state `-v`
  auto-creates a missing source **as a directory**; `--mount` fails loudly
  instead. Given the file-vs-directory subtlety above, fail loudly.
- **Permissions**: `0700` on the directory, owned by the uid the container runs
  as (`connect()` needs write permission on the socket inode). Mount `ro` so the
  container cannot unlink or replace the socket.

Optionally add a per-Run token as a **cross-check, not as the identity**: the
Broker verifies the token presented on listener L is the one it minted for L's
Run. It defends against a spawn-path bug mounting the wrong directory, costs
~10 lines, and makes a mismatch a loud alarm. Reasonable belt-and-braces; the
socket is doing the real work.

### The constraint this puts on ticket 05

The wire protocol must be **name-addressed**: `{"names": ["GITHUB_PAT"]}`, with
the Broker looking up ciphertext itself from what the control plane gave it at
spawn. Never `{"ciphertext": "..."}` — a Broker that decrypts whatever it is
handed is a decryption oracle, and every Agent YAML sits in a git repository the
Run holds a credential for. That is precisely the failure demonstrated against
`sops keyservice`, and it would reappear in our own code just as easily.

## 5. The minimum viable Broker in Go

Roughly 150 lines. The sketch below **compiles clean for `linux/amd64` and passes
`go vet`** against `filippo.io/age v1.3.2` — the API calls are verified, not
invented. Ticket 05 settles the wire format; this establishes feasibility and
size.

```go
type req struct {
    Names []string `json:"names"`
}
type resp struct {
    Values map[string]string `json:"values,omitempty"`
    Error  string            `json:"error,omitempty"`
}

// What the control plane hands the Broker when it starts a Run.
// This struct IS the caller's identity: it is bound to one listener.
type run struct {
    RunID     string
    Agent     string
    Allowlist map[string]bool // from the Agent YAML
    Cipher    []byte          // armored age blob from the Agent YAML
}

// One listener per Run. Identity is the socket, not anything the caller says.
func (b *broker) serveRun(dir string, r run) error {
    if err := os.MkdirAll(dir, 0o700); err != nil {
        return err
    }
    sock := filepath.Join(dir, "broker.sock")
    os.Remove(sock)
    l, err := net.Listen("unix", sock)
    if err != nil {
        return err
    }
    go func() {
        defer l.Close()
        for {
            c, err := l.Accept()
            if err != nil {
                return
            }
            go b.handle(c, r) // r is the identity, closed over
        }
    }()
    return nil
}

func (b *broker) handle(c net.Conn, r run) {
    defer c.Close()
    var q req
    if err := json.NewDecoder(io.LimitReader(c, 1<<16)).Decode(&q); err != nil {
        json.NewEncoder(c).Encode(resp{Error: "bad request"})
        return
    }
    for _, n := range q.Names {
        if !r.Allowlist[n] {
            b.audit.Error("denied", "run", r.RunID, "agent", r.Agent, "name", n)
            json.NewEncoder(c).Encode(resp{Error: "not in allowlist: " + n})
            return // fail the whole invocation, loudly
        }
    }
    all, err := b.decrypt(r.Cipher) // age.Decrypt with the Runner's identity
    if err != nil {
        b.audit.Error("decrypt failed", "run", r.RunID, "err", err)
        json.NewEncoder(c).Encode(resp{Error: "undecryptable by this Runner"})
        return
    }
    out := map[string]string{}
    for _, n := range q.Names {
        v, ok := all[n]
        if !ok {
            json.NewEncoder(c).Encode(resp{Error: "unknown name: " + n})
            return
        }
        out[n] = v
    }
    b.audit.Info("granted", "run", r.RunID, "agent", r.Agent, "names", q.Names)
    json.NewEncoder(c).Encode(resp{Values: out})
}
```

Note what is absent: no authentication, no token table, no `/proc` parsing, no
peer credentials, no platform-specific files. `r` is closed over by the listener,
so the Allowlist check cannot be bypassed by anything the caller sends.

The `dsecrets` half is smaller. The decision that matters is `syscall.Exec`
rather than fork — the child *replaces* `dsecrets`, so SIGTERM reaches the agent
process and Teardown fires. **Measured** (see [the survey](#the-survey)): with
exec-replacement the agent stays PID 1 and `docker stop` reaches it; with sops's
fork the child is orphaned.

```go
env := os.Environ()
for k, v := range r.Values {
    env = append(env, k+"="+v)
}
bin, err := exec.LookPath(argv[0])
if err != nil {
    return err
}
// exec, not fork: SIGTERM must reach the agent process, or Teardown never runs.
return syscall.Exec(bin, argv, env)
```

Plaintext exists only in `dsecrets`'s memory between the reply and the `Exec`,
and thereafter only in the child's environment — which is what ADR-0001 promises.

## 6. Consequences for ADR-0001 and `map.md`

Nothing is invalidated. All three refinements are additive.

**1. ADR-0001's "Every decryption passes through the broker and can be logged"
needs one more clause to hold.** As written, a Broker that decrypts *submitted
ciphertext* satisfies the sentence while providing no audit value at all — the
sops keyservice satisfies it and logs nothing but `Decryption succeeded`. The
Broker must be **name-addressed** for the claim to mean anything. Worth a line in
the ADR, because it is the constraint most likely to be lost between here and
implementation.

**2. ADR-0001's known ceiling is slightly sharper than stated.** It says a Run
"can still capture a secret deliberately — by wrapping a command that prints it".
**Measured**: it does not need to wrap anything. Any same-uid process in the
container can read `/proc/<child-pid>/environ` while the child runs. This does
not change the decision — the ADR already accepts the boundary "does not contain
a hostile agent" — but "the child's environment is readable by its siblings" is
more precise, and someone will eventually assume otherwise. Running the child as
a different uid does block it, at the cost of the agent being unable to interact
with the process it spawned.

**3. `map.md`'s "Secret rotation and Runner enrolment" gets easier with one blob
per Agent** — re-encryption on enrolment is then one age file per Agent rather
than one per secret. Noted only because it currently sits under "Not yet
specified".

## Facts checked and their versions

| Fact | How | Version |
|---|---|---|
| `Encrypt`/`Decrypt` variadic multi-recipient | source + executed | `filippo.io/age` v1.3.2 |
| Decryption with any one of N identities | executed | v1.3.2 |
| `NoIdentityMatchError` on a non-recipient | executed, `errors.As` | v1.3.2 |
| `ParseIdentities` reads `age-keygen` output | executed | v1.3.2 / `age` CLI v1.3.1 |
| `age` CLI ciphertext decrypts via the library | executed | CLI v1.3.1 → lib v1.3.2 |
| Ciphertext sizes, 1–4 recipients | measured | v1.3.2 |
| Size formula matches the spec exactly | derived + measured | [C2SP age.md](https://github.com/C2SP/C2SP/blob/main/age.md) |
| Per-secret vs per-Agent blob sizing | measured | v1.3.2 + `gopkg.in/yaml.v3` v3.0.1 |
| YAML `\|`/`\|-`/`>`/whitespace/CRLF behaviour | executed | v1.3.2 + yaml.v3 v3.0.1 |
| Binary ciphertext is not valid UTF-8 | executed | v1.3.2 |
| `sops exec-env` puts values in the child env | executed | sops 3.13.3 |
| `sops keyservice` on a unix socket holds the age key | executed | sops 3.13.3 |
| **sops keyservice decrypts an unrelated file** | executed | sops 3.13.3 |
| **`sops exec-env` orphans the child on SIGTERM** | measured | sops 3.13.3 |
| Per-Agent keyservices give crypto isolation | measured | sops 3.13.3 |
| `encrypted_regex` + `mac_only_encrypted` keeps YAML hand-editable | measured | sops 3.13.3 |
| `DecryptRequest{Key, ciphertext}` — no names, no caller | [keyservice.proto](https://github.com/getsops/sops/blob/main/keyservice/keyservice.proto) | main |
| age plugins are subprocesses; unit of work is the file key | source (`plugin/client.go`) | v1.3.2 |
| summon sets `runner.Env`, forwards signals | source (`pkg/summon/subcommand.go`) | v0.11.0 |
| `SO_PEERCRED` → host PID when broker in host PID ns | measured | Docker 29.4.0, kernel 7.0.14 |
| `/proc/<pid>/cgroup` matches `docker inspect` id | measured | Docker 29.4.0 |
| `SO_PEERCRED` → `pid=0` across sibling PID namespaces | measured | Docker 29.4.0 |
| uid is 0 for every root container (no userns-remap) | measured | Docker 29.4.0 |
| PID-reuse race: `/proc/<pid>` gone before lookup | measured | Docker 29.4.0 |
| PID visible in ancestor ns; `pid_vnr` uses reader's ns | `pid_namespaces(7)`, `kernel/pid.c` | man-pages 6.18 |
| `/proc/pid/cgroup` path is relative to reader's cgroup ns | `cgroup_namespaces(7)` | man-pages 6.18 |
| `SO_PEERPIDFD` added in Linux 6.5, undocumented in man-pages | kernel source, diffed v6.4/v6.5 | 6.5 |
| SPIRE's PID-reuse mitigation + "authoritative source" comment | source (`peertracker`, `containerinfo`) | spiffe/spire main |
| File bind-mount pins the inode; dir mount survives | measured | Linux, `mount --bind` |
| exec-replacement keeps agent at PID 1; `docker stop` reaches it | measured | Docker 29.4.0, alpine 3 |
| macOS host socket bind-mount into a container fails | measured | OrbStack, Docker 29.4.0 |
| `/proc/<pid>/environ` readable by same-uid siblings | measured | Docker 29.4.0 |
| Broker + `dsecrets` sketch compiles and vets | `GOOS=linux go build` / `go vet` | Go 1.27.0, age v1.3.2 |
| Docker secrets are swarm-only | [docs.docker.com](https://docs.docker.com/engine/swarm/secrets/) | current |
| systemd `LoadCredential=` incl. the AF_UNIX socket form | systemd.exec(5), CREDENTIALS.md | current |

Host: macOS 15 (darwin/arm64), Go 1.27.0. All container measurements ran on real
Linux inside Docker (OrbStack, kernel 7.0.14).

## Open / UNKNOWN

- **A macOS host cannot bind-mount a unix socket into a container.** No longer
  unknown — **measured**: the socket is visible in the container and `stat`s as a
  socket, but `connect()` gives `ECONNREFUSED`. This is a *development
  ergonomics* problem only, since both Runners are Linux. Local dev must either
  run the Broker in a container sharing a Docker volume (measured working) or use
  a Linux VM. Worth stating in ticket 08.
- **The cgroup path format on the actual Runners is UNKNOWN.** Measured
  `0::/../<id>` under OrbStack; expect `/system.slice/docker-<id>.scope` under
  systemd. Only matters if the `SO_PEERCRED` route is taken against this
  recommendation. Settled by running the measurement on the real `macmini` and
  Coolify hosts.
- **OpenBao / Infisical specifics are second-hand.** They came via a sub-agent
  that explicitly flagged them as not personally verified. They do not change the
  recommendation, but if OpenBao is ever reconsidered, re-verify the declarative
  `audit` stanza's immutability first — that is the claim the "OpenBao over
  Vault" preference rests on.
- **Whether one age file per Agent survives ticket 04's schema is not settled
  here.** It is right on size and rotation, but if a future Agent needs two
  secrets with genuinely different recipient sets, the schema needs a list of
  blobs rather than one. Cheap to allow later; noted so the choice is deliberate.
- **Broker restart bookkeeping is untouched** — whether the Broker re-reads Agent
  YAML on restart or the control plane re-registers live Runs. Ticket 05's
  territory, flagged because the directory bind-mount finding makes Broker restart
  a *supported* operation and therefore a real code path that needs a decision.
