# 5. Journals live in object storage, not git

Date: 2026-08-31

## Status

Accepted

## Context

The Journal was originally conceived as a private git repository — one
repository for every Agent, a directory per Run, committed and pushed at
Teardown. Git is familiar, browsable on the web, and already in the stack for
the work branches.

The problem is Teardown's budget. Pushing to a git repository requires cloning it
first, and that cost grows with every Run ever recorded — tens of thousands of
directories after a busy year. The Journal write is the operation most likely to
be racing a `SIGKILL` at the end of a 90-second grace period, and git is the one
backend whose cost at that moment increases for the entire life of the system.

## Decision

Journals are written to **S3-compatible object storage**. The specification names
the protocol rather than a vendor; Cloudflare R2 is the default, and MinIO on the
control plane's own host is an equivalent choice.

Two objects per Run: a small, separately-fetchable `meta.json`, and a
`run.tar.zst` holding the event stream, stderr, and any CLI rollout files.

Access is by **presigned PUT URL**, requested from the Run API when Teardown
begins. No storage credential enters a container.

The work branch remains in git, because that is real code.

**Update, 2026-09-03.** MinIO's community repository was archived on
2026-04-25 and its images are unmaintained. The decision is unchanged, since
it names the protocol; the sandbox and any self-hosted choice use an
S3-compatible server that is still maintained (RustFS in `sandbox/`).

## Consequences

Object PUTs cost the same on the first Run as on the hundred-thousandth, so
Teardown's most dangerous moment stops getting slower over time.

Several problems disappear rather than being solved. Retention becomes an object
lifecycle rule instead of a policy to implement. Deleting a leaked secret becomes
a `DELETE` instead of a history rewrite. Concurrent writes from many Runs across
both Runners need no retry logic, because no two Runs write the same key.
`ListObjects` is the index, so no shared index file exists to contend over — and
a shared index file would have been the only thing two Runs ever both touched.

Presigned URLs mean a compromised Run cannot reach another Run's record, which a
bucket-wide credential in the container would have allowed.

What is lost is browsing on github.com, and `git log`. Neither was providing
tamper-evidence — the Run holds write access under either backend.

The two-object split is what keeps the data analysable: `meta.json` can be
fetched and grepped across thousands of Runs without unpacking a single archive,
which is the stated purpose of keeping Journals at all.
