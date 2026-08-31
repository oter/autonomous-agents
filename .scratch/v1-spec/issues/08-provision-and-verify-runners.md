# Provision the Runners and verify `ssh://` end to end

Type: task
Status:
Blocked by: 03, 10

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

> **From ticket 03:** §7 of
> [`research/03-docker-sdk-over-ssh.md`](../research/03-docker-sdk-over-ssh.md)
> has 27 numbered commands to run on the real Mac mini, grouped A–E. Run group A
> first — it settles the `PATH` trap, which is the default failure, not an edge
> case. Blocked additionally by ticket 10: do not provision a daemon before the
> daemon has been chosen.

## Revised checklist

The original question above is **stale in three places** and this section
supersedes it. Do not provision an age identity per Runner — ADR-0003 left the
control plane as the only holder of key material. Do not create an `agentruns`
git repository — ticket 07 moved Journals to object storage. The `ssh://`
transport was replaced by a forwarded socket in ticket 10, so the `PATH` trap is
no longer relevant and the Mac needs no `docker` CLI at all.

### Provision

- **Tailscale on both hosts.** Record the control plane's tailnet IP — the Run
  API and the egress allow-rule both need it.
- **A Docker daemon on the Mac.** Operator's choice; record the **socket path**,
  which is the only value the choice actually determines. If it is Docker
  Desktop, pin ≥ 4.88.0 for the engine-shutdown fix.
- **`sshd` on the Mac** with `AllowStreamLocalForwarding yes` (the default), plus
  a passphrase-less key for the `autossh` sidecar and a `known_hosts` entry.
- **An S3-compatible bucket** (R2 by default) and credentials **for the control
  plane only** — containers get presigned URLs, never a key.
- **A fine-grained PAT** scoped to the repositories Agents will push work
  branches to.

### Verify — in this order

1. **The tunnel.** `autossh -N -L /tmp/mac.sock:<remote-socket> runner@macmini`,
   then `docker -H unix:///tmp/mac.sock version` from the control plane host.
2. **THE LOAD-BEARING TEST.** Through that forwarded socket, start a container
   whose entrypoint traps `SIGTERM`, sleeps, and exits 0. `docker stop --time=30`
   it and confirm the trap **ran to completion** and the exit code is **0, not
   137**. If this fails, Teardown never fires on a timeout kill and every
   timed-out Run loses its Journal — which reopens the Runner design rather than
   getting worked around.
3. **Engine shutdown.** Reboot the Mac with nobody logged into the GUI, wait
   three minutes, and check whether the daemon came back. Then confirm a running
   container is not truncated to ~2s of grace when the engine restarts.
4. **`NET_ADMIN` and iptables inside a container on the Mac's daemon.** Ticket 11
   enforces egress rules from inside the container precisely so both Runners
   behave alike — verify that actually holds on Docker Desktop's or Colima's VM.
   This is new and untested.
5. **A severed link.** Cut the SSH connection mid-container and confirm the
   container survives, that `autossh` reconnects, and that the control plane can
   reattach and read the authoritative exit code from `ContainerInspect`.

§7 of [`research/03-docker-sdk-over-ssh.md`](../research/03-docker-sdk-over-ssh.md)
has 27 numbered commands. Groups B–E still apply verbatim; **group A is now
obsolete** — it tested the `PATH` trap, which the forwarded socket removed.

The answer records what was done plus any fact later work depends on: the tailnet
IP, the socket path, the daemon and its version, and where each credential lives.
