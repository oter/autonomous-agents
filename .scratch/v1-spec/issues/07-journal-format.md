# The Journal format

Type: grilling
Status: resolved
Blocked by: 01, 06

## Question

Specify what a Run leaves behind, file by file, inside
`agentruns/<agent>/<run-id>/`.

The stated purpose is analysing behaviour across Runs in order to tune
execution, so the format's real test is: can a future session read a hundred of
these and find a pattern? Design for the reader, not the writer.

- What files exist, and what is in each. At minimum: what the Run was asked to
  do, the trigger payload, the raw event stream from ticket 01, the outcome, and
  enough metadata to correlate with the control plane's own record.
- What metadata is worth capturing at start — Agent name and file hash, Runner,
  trigger kind and source, CLI name and version, limits in effect, skills
  installed and their versions.
- What is worth capturing at the end — exit status, whether a limit was hit,
  duration, turns used, work branch pushed, and any cost or token figures if
  ticket 01 found they are available.
- Is the top-level index (`runs.jsonl`) worth having, and what one line per Run
  needs to contain for it to be useful.
- Concurrency: many Runs across two Runners pushing to one repository will
  collide. Decide the strategy — pull-rebase-retry, one branch per Run, or
  something else — and make sure it cannot lose a Journal.
- What must never appear in a Journal, and who is responsible for keeping it out.

> **From ticket 01:** the `--json` stream is rich enough (8 event types, 9 item
> types; schema in the research doc). Parser traps: `web_search` serialises `id`
> twice, `declined` patches fold into `failed`, and `error` events carry no
> `will_retry` so retry noise is shape-identical to a fatal error. Rollout dirs
> are local-time while line timestamps are UTC, and `codex exec` writes
> `history_mode: paginated` with PascalCase `TurnItem` tags.

## Answer

### The backend is object storage, not git

The `agentruns` git repository from charting is **dropped**. Journals go to
**S3-compatible object storage** — Cloudflare R2 as the default, though the spec
names the protocol rather than the vendor, so MinIO on the Coolify host works
identically if zero external dependencies is ever wanted.

The decisive argument is Teardown's budget. A git push requires a clone first,
and that cost grows with every Run ever recorded — tens of thousands of
directories after a year. The Journal write is the operation most likely to be
racing a `SIGKILL`, and git is the one backend that gets slower the longer the
system runs. Object PUTs are flat forever.

Everything else follows: lifecycle rules make retention a configuration value
rather than a policy to implement; deleting a leaked secret is a `DELETE` instead
of a history rewrite; concurrent writes need no retry logic at all, so the
pull-rebase-retry question disappears; and `ListObjects` **is** the index, which
removes `runs.jsonl` for a second, independent reason.

What git was providing: browsing on github.com. Not tamper-evidence — the Run
holds write credentials either way.

**The work branch stays git.** That is real code in a real repository; only the
Journal moves.

### Layout

```
<agent>/<run-id>/meta.json        small, flat, stable schema
<agent>/<run-id>/run.tar.zst      event stream, stderr, codex rollouts
```

Two objects, a fixed count regardless of how many rollout files a Run produces.
`meta.json` stays separately fetchable so metadata can be grepped across a
thousand Runs **without unpacking any of them** — that is the two-tier design
surviving the change of backend. The `<agent>/` prefix makes per-Agent listing
chronological for free, since the run id begins with its timestamp.

### Access: presigned PUTs, no credential in the container

Teardown calls the Run API, receives two presigned PUT URLs, and uploads. **No
storage credential ever enters a container**, consistent with ADR-0003. The URLs
are minted when Teardown starts rather than at spawn, so a ninety-minute Run
cannot outlive them.

A bucket-wide access key delivered through `dsecrets` was rejected: it would let
any Run overwrite any other Run's Journal. Posting the Journal through the
control plane was rejected for putting it back in the Journal path and streaming
megabytes through it for nothing.

This adds one endpoint to ticket 11 — read-only about the Run itself, which fits
that API's governing principle.

### `meta.json`

**At start**, the things that let a behaviour change be correlated with a
configuration change, which is the entire stated purpose: Agent name **and the
SHA-256 of its YAML**, Runner, trigger kind and name, CLI name **and version**,
base image digest, the limits actually in effect, resolved skill versions, and
the prompt and personality verbatim.

**At end**: exit code; `terminal_reason` read from **the event stream, not
`$?`** — sandbox denials exit 0 and `error_seen` is sticky, so the exit code
alone lies (ticket 01); duration; the work branch pushed and whether that
succeeded; any throttle events from the Run API; and cost — `total_cost_usd` for
claude, **tokens only for codex**, because that divergence is real and the schema
should not pretend the two are the same number.

### Parser notes for whoever reads these

From ticket 01, and easy to get wrong: `web_search` serialises `id` twice;
`declined` patches are folded into `failed`; `error` events carry no `will_retry`,
so retry noise is shape-identical to a fatal error. Codex rollout directories are
local-time while the line timestamps inside are UTC, and `codex exec` writes
`history_mode: paginated` whose `TurnItem` tags are PascalCase while everything
else is snake_case. Teardown collects **both `*.jsonl` and `*.jsonl.zst`**,
because codex compresses rollouts when cold.

### What must never appear, and who keeps it out

**Nobody keeps it out, and that is the accepted answer.** `dsecrets` keeps
plaintext out of the agent's environment, but nothing prevents an agent running
`dsecrets FOO -- sh -c 'echo $FOO'` and putting the value straight into the event
stream. Nothing inside the container knows the values to scrub — `dsecrets` hands
them to a *child* and the entrypoint never sees them.

Having the control plane scrub was considered: it is the decryptor, so it does
know every value, and its audit log already records which names each Run fetched.
Rejected because it would mean the control plane reading and rewriting Journal
content, and it only helps if it inspects every object on the way past.

So: **private bucket, ceiling recorded.** This is the first thing to revisit if
the Journal is ever shared with anyone.

### Deliberately out of scope

**No work diff in the Journal** — it is a branch in its own repository, and
duplicating it doubles the storage to say the same thing.

**Retention is deferred**, but it stopped being hard: object lifecycle rules
express it as configuration. Choosing the actual policy needs data this system
has not produced yet, so guessing now would be guessing.
