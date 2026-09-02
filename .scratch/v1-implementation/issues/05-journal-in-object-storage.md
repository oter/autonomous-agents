# 05: Journal in object storage

**What to build:** Every Run's Journal lands durably in the S3-compatible bucket under the SPEC §10 layout, and a finished Run can be fully explained from the bucket alone: what configuration produced it, what it did, how it ended, what it cost.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** resolved

- [x] Journal-urls endpoint returns presigned PUT URLs for meta and archive; bucket credentials never enter the container
- [x] Teardown uploads `meta.json` and the zstd tarball; upload failure doesn't mask the Run's real exit code
- [x] `meta.json` at start: Agent name and the SHA-256 of its YAML, Runner, trigger kind and name, CLI name and version, base image digest, limits in effect, resolved skill versions, prompt and personality verbatim — skill versions with ticket 11, which is what installs skills
- [x] `meta.json` at end: exit code; `terminal_reason` read from the event stream, not `$?` (sandbox denials exit 0; `error_seen` is sticky); duration; work branch and push result; throttle events; cost as `total_cost_usd` for claude and tokens-only for codex — the divergence recorded, not papered over — the work fields are `null`/`none` slots until ticket 11 pushes
- [x] Event-stream parsing honors the measured quirks in SPEC §10's parser notes (duplicate `web_search` ids, `declined`→`failed` folding, retry noise shape-identical to fatal errors, codex local-time directories vs UTC timestamps, PascalCase `TurnItem` tags)
- [x] ListObjects is the index — no index file, nothing two Runs both write
- [x] No secret scrubbing is attempted; the bucket is private (accepted ceiling, per SPEC §10)

## Answer

One new file in `internal/run`, one jq filter in the image, no new dependency
and no new package. The Journal is written where ADR-0004 says (by the
container, at Teardown) to where ADR-0005 says (a bucket, by presigned PUT).

- **Presigned URLs** (`internal/run/journal.go`): `Bucket.Presign` is SigV4's
  query-string form in forty lines of stdlib, pinned by the worked example in
  the S3 API reference (the `AKIAIOSFODNN7EXAMPLE` GET whose signature is
  `aeeed9bb…`), so the arithmetic is checked against a number this code did
  not produce. Path-style `<endpoint>/<bucket>/<key>`, `UNSIGNED-PAYLOAD`,
  `host` the only signed header, 15 minutes of validity. An AWS SDK would be
  a heavyweight module for one function. The credential is
  `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` in the control plane's
  environment, required at startup like `journal.endpoint` and
  `journal.bucket` now are in the config; it never leaves the control plane,
  and the API test asserts the secret is not in a URL.
- **`GET /run/journal-urls`** (`api.go`) answers `{meta, archive,
  throttle_events}`: the two PUT URLs under `<agent>/<run-id>/`, minted when
  Teardown asks so a long Run cannot outlive them, plus the Run's throttle
  count, the one at-end fact `meta.json` records that only the control plane
  holds. Still read-only about the Run itself; the routing probe now expects
  405 on `POST`.
- **`meta.json`** is assembled by the entrypoint's `write_meta` from three
  sources. `RUN_META`, a JSON environment variable the Spawner sets with what
  only it knows: `agent_sha256` (the config loader hashes the file bytes),
  `runner`, `trigger_kind`/`trigger_name` (a `run.Trigger` now travels with
  `Start` and sits on the Run record; the UI's is `run.RunNow`), the image
  tag, its content id and, when pulled, its registry digest from `GET
  /images/{ref}/json`, and the limits. The entrypoint's own: `cli_version`
  from `$AGENT_CLI --version`, prompt and personality verbatim, timestamps,
  duration, exit code, the work slots. And `stream.jq`'s summary of
  `stream.jsonl`: `terminal_reason`, `is_error`, `error`, `num_turns`,
  `total_cost_usd`, `usage`, `permission_denials`, `error_events`,
  `failed_items`, `events`. The schema is in SPEC §10, with the rule that a
  field the CLI does not report is `null`, never zero: codex has no dollars,
  claude has no top-level error events.
- **`image/stream.jq`** honours the parser notes and is tested from Go
  (`image/stream_test.go`, eleven cases built from the measured shapes in
  the research doc, trimmed to the fields the filter reads): the
  auth-failure trap (subtype `success`, `is_error` true, `terminal_reason`
  `api_error`); five `Reconnecting… n/5` lines before a completed turn
  (`error_events` 5, outcome still `completed`); `turn.failed` with the
  budget message; a declined patch counted as `failed_items`, named for what
  codex actually says; the doubled `web_search` `id` parsing last-wins;
  usage summed key by key over the union of keys across turns; and a torn
  last line for either CLI yielding `no_terminal_event` and null cost rather
  than nothing. Lines are parsed one at a time (`-R`, `fromjson?`) so a Run
  killed mid-write still gets a summary. The two rollout-file notes
  (local-time directories, PascalCase `TurnItem`) are honoured by not going
  near them: Teardown copies the whole state tree and archives rollouts
  rather than parsing them.
- **Teardown** (`image/entrypoint.sh`): `set +e` first, so no failed step can
  end the function before `exit "$rc"`. Order: `journal-urls`, `write_meta`,
  `tar`, the two PUTs. `api` now carries `--retry 3`, so a Run that drained
  its token bucket in its last seconds waits out the `Retry-After` instead of
  losing its Journal to a 429. A `write_meta` that fails removes its output,
  so an empty `meta.json` is never uploaded. The finished report's message is
  `journal uploaded` when both PUTs returned 2xx, else `journal upload
  failed: meta` / `archive`, or `journal upload skipped` when the control
  plane could not be reached; the exit code is the Run's whatever the upload
  did. SPEC §6's script and bullets are updated to match, and `report` takes
  the exit code, closing the divergence ticket 03 noted.
- **Container removal** (`spawn.go`): when Docker reports the exit, the poller
  `HEAD`s both objects through presigned URLs of its own and removes the
  container only if both are there; otherwise it logs the container's last
  message and keeps it as the only copy, readable with `docker cp`. The
  message is never what decides: the agent holds the same token and could
  report anything. Ticket 03's accumulation shortcut is closed for every
  uploaded Run.

### Tests

Unit (`go test ./...`): the AWS worked example and the path-style layout;
`journal-urls` end to end through the middleware (URLs, expiry, no
credential, the Run's throttle count, 405); the eleven `stream.jq` cases;
`Agent.SHA256`, `journal.*` required, region default; `RUN_META` in the
create env with the fake Docker answering the image inspect; the poller
`HEAD`ing both objects and removing the container when a bucket has them,
and keeping it when nothing does.

### Demo (2026-09-02, OrbStack, `agent-base:dev` rebuilt with `stream.jq` and the new entrypoint, no subscription token in the shell)

`AA_IMAGE=agent-base:dev go test ./internal/run -run WalkingSkeleton -v`
now uploads to an in-process fake bucket and reads the Journal back from it
instead of `docker cp`. All three cases pass in about 30s:

- trivial claude Run: exit 1, finished report `journal uploaded`, both
  objects present, archive lists `stream.jsonl`, `stderr.log`, the
  transcript, `meta.json`; `meta.json` says `terminal_reason` `api_error`,
  `is_error` true, `error` "Not logged in · Please run /login",
  `cli_version` "2.1.258 (Claude Code)", `image_id` `sha256:…`,
  `agent_sha256` as given, `throttle_events` 0; container removed after the
  control plane's `HEAD`s.
- codex Run with `wall_clock: 5s`: exit 143, `no_terminal_event`,
  `error_events` 3 (its 401 retries), `total_cost_usd` null, `usage` null,
  archive with the rollout; container removed.
- codex Run whose Run API vanished after the payload fetch: nothing in the
  bucket, container kept, its local `meta.json` has exit 143 and
  `throttle_events` null.

Then against a real S3 implementation that validates signatures: MinIO
(`docker run -p 9000:9000 minio/minio server /data`, the bucket created with
`curl --aws-sigv4`), and the real binary run the way Coolify will run it, as
a container with the Docker socket mounted, so that `host.docker.internal`
names the same bucket for it and for the Runs. Run-now on a claude `hello`
and a codex `sleepy` (5s wall clock): four objects landed, both `meta.json`
fetched back with the contents above, the archive listed through `tar
--zstd -t` in the image, and both containers removed on the poll after
their exit, after MinIO answered the control plane's `HEAD`s. MinIO would
have answered 403 to a bad signature; it answered 200 to every request.

### Deliberate shortcuts, all marked `ponytail:` in code

- `cli_version` costs one CLI start, about a second, before the payload
  fetch (`entrypoint.sh`). Baking versions into the image at build time is
  the upgrade.
- The stream summary is taken right after `kill -TERM` without waiting for
  the agent to exit (`entrypoint.sh`); in the kill path there is no terminal
  event to wait for, and waiting would spend grace. A bounded wait is the
  upgrade.
- `journal.region` is an unvalidated string (`load.go`).

### Follow-ups for later tickets

- Ticket 11 replaces `w=none` with `push_work && w=pushed || w=failed`, fills
  `work_branch`, and adds the resolved skill versions to `meta.json`; the
  slot is `RUN_META` or the entrypoint, whichever knows them.
- Tickets 07 and 08 pass their own `run.Trigger` to `Start` (webhook by
  path, schedule by cron). Ticket 12 reads `Trigger` off the Run record.
- The published image must carry `stream.jq` and the new entrypoint before
  a deploy points at it: `ghcr.io/oter/agent-base:2026-09-05`, the date tag
  pushed with this commit.
- Retention is a lifecycle rule on the bucket, still to be set.

### Review (two-axis, Standards and Spec, before commit)

Acted on:

- **A 429 at Teardown lost the Journal** (Spec): `api` had no retry, so a
  Run that drained its bucket got `urls='{}'` and skipped the upload, and
  SPEC §9 promises the flagged Run appears in its Journal. `--retry 3`;
  curl honours `Retry-After` and does not retry a refused connection.
- **Removal on the container's own claim** (Spec, Standards): the agent
  holds `RUN_TOKEN`, so a forged `finished` with the magic message would
  have made the Run terminal, blocked Teardown's upload with a 403, and had
  the control plane delete the only copy. Removal now follows the control
  plane's own `HEAD` of both objects; the message is logged, not trusted.
- **An empty `meta.json` was uploadable** (Standards): the redirect
  truncated the file before a failing jq could say so. The write removes its
  output on failure, and the missing file makes the PUT fail honestly.
- **Usage summed over the first turn's keys only** (Standards): a key first
  seen in a later turn was dropped. Summed over the union; a test pins it.
- **`image_digest` was the content id** (Spec): now `image_id`, with
  `image_digest` the registry digest when the image was pulled.
- **`work_branch` missing from the schema** (Spec) while §10's prose asks
  for it: a `null` slot, so the schema does not change under ticket 11.
- **Trigger vocabulary** (Standards): CONTEXT.md named two kinds; run now is
  the third, `manual`, and the glossary says so.
- **SPEC sync** (Standards): `touch`, `CLI_VERSION`, `--retry`, the
  `HEAD`-before-removal rule, `unparsed`, and a null `terminal_reason` on a
  present `result` event are all in §6/§10 now.
- Test hygiene (Standards): throttle count asserted against the Run's own
  count rather than a timing-sensitive literal; `strings.HasPrefix`; a
  finished comment; `run.RunNow` instead of five literals; `omitempty` for
  the strings; the orphan case checks the container once instead of polling
  ten seconds for a removal that must not happen.
- The ticket's word "verbatim" about the fixtures (Spec): they are built
  from the measured shapes and trimmed; the file and this answer now say so.

Dismissed, with the reason:

- *`throttle_events` on `journal-urls` widens the API to fit the code*
  (Spec). It is the one at-end fact `meta.json` must carry that only the
  control plane holds, it is read-only about the Run itself, and SPEC §9
  says why. The alternative was a third object in the bucket, which §10
  forbids.
- *A `result` event with `terminal_reason: null` is indistinguishable from
  "not reported"* (Spec). It is exactly "not reported", by the CLI; the
  summary keeps the CLI's value and `num_turns` shows the event existed.
  Documented in §10 rather than invented.
- *`fakeBucket`'s read branch was unexercised* (Standards). It is now: the
  control plane's `HEAD`s go through it.
- *`stream_test.go` is `package image`* (Standards): it is `image_test`
  now, which Go accepts for a directory with no non-test files.
