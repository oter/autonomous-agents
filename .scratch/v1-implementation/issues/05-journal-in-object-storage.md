# 05: Journal in object storage

**What to build:** Every Run's Journal lands durably in the S3-compatible bucket under the SPEC §10 layout, and a finished Run can be fully explained from the bucket alone: what configuration produced it, what it did, how it ended, what it cost.

**Blocked by:** 03 (Walking-skeleton Run).

**Status:** ready-for-agent

- [ ] Journal-urls endpoint returns presigned PUT URLs for meta and archive; bucket credentials never enter the container
- [ ] Teardown uploads `meta.json` and the zstd tarball; upload failure doesn't mask the Run's real exit code
- [ ] `meta.json` at start: Agent name and the SHA-256 of its YAML, Runner, trigger kind and name, CLI name and version, base image digest, limits in effect, resolved skill versions, prompt and personality verbatim
- [ ] `meta.json` at end: exit code; `terminal_reason` read from the event stream, not `$?` (sandbox denials exit 0; `error_seen` is sticky); duration; work branch and push result; throttle events; cost as `total_cost_usd` for claude and tokens-only for codex — the divergence recorded, not papered over
- [ ] Event-stream parsing honors the measured quirks in SPEC §10's parser notes (duplicate `web_search` ids, `declined`→`failed` folding, retry noise shape-identical to fatal errors, codex local-time directories vs UTC timestamps, PascalCase `TurnItem` tags)
- [ ] ListObjects is the index — no index file, nothing two Runs both write
- [ ] No secret scrubbing is attempted; the bucket is private (accepted ceiling, per SPEC §10)
