# 15: Local sandbox

**What to build:** A way to spin the whole setup up on a laptop: the control plane, a bucket, sample Agents, and a way to start a Run and see its Journal land, without retyping the ticket 05 demo.

**Blocked by:** 05 (Journal in object storage).

**Status:** resolved

- [x] `docker compose up` gives a running control plane and a bucket; a Run can be started with one `curl`
- [x] The control plane runs the way Coolify will: as a container with the Docker socket
- [x] The Journal is visible in a browser and from the shell
- [x] Model credentials pass through from the shell; nothing secret is committed

## Answer

`sandbox/`: a compose file (MinIO with a health check, a one-shot bucket
creation with the `mc` the MinIO image ships, the control plane built from
the repository's new root `Dockerfile` with the socket mounted and ports
8081/8082 published), a checked-in `control-plane.yaml` (UI password
`sandbox`, image `autonomous-agents/agent:dev`, everything reached over
`host.docker.internal`), the two demo Agents, and a README. The root
`Dockerfile` is the control plane's own image, for ticket 14 as well.

Asked for on 2026-09-03 with the fair point that a `/tmp` demo the agent
deletes afterwards is not something the user can spin up. Verified the same
day on OrbStack: `up --build`, two run-nows, four objects in MinIO, both
containers removed, `down -v`.

Known limit, in the README: a plain Linux daemon does not give Run
containers `host.docker.internal`; use a LAN or tailnet address there.
