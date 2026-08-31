# Provision the Runners and verify `ssh://` end to end

Type: task
Status:
Blocked by: 03

## Question

Nothing to decide here — but the two-Runner design rests on an unverified
assumption, and ticket 03 can only settle it on paper. This ticket settles it on
the actual hardware.

Scoped deliberately to what unblocks a decision, not to standing the system up.

- Docker reachable on the local Linux host, and SSH access to the Mac mini with
  Docker Desktop running.
- Run the checks ticket 03 identified as risky, against the real Mac mini:
  create a container with resource limits over `ssh://`, follow its logs, then
  `docker stop --time=30` it and confirm the container received `SIGTERM` and had
  its full grace period before dying.
- Confirm what happens to a running container when the SSH link is severed
  mid-stream.
- Create the private `agentruns` repository and the fine-grained PAT, and record
  where the credential lives — later tickets reference it.
- Generate an age identity per Runner and record the recipient strings; ticket 04
  needs them to write real ciphertext into its example YAML.

The answer records what was done plus any fact later tickets depend on. If
`ssh://` turns out not to hold, say so plainly — that reopens the Runner design
rather than getting worked around.

> **Constraint from ticket 02:** macOS cannot bind-mount a unix socket into a
> container. The Mac mini Runner runs its Broker containerised on a shared Docker
> volume; the local Runner uses a host directory. Bind-mount the *directory*, never
> the socket file — a file mount pins the inode and a Broker restart leaves running
> containers on `ECONNREFUSED`.
