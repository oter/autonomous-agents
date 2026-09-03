# Sandbox

Run the control plane on your own machine, with MinIO as the Journal bucket.
The control plane runs as a container with the Docker socket, the same way
Coolify runs it. Runs are containers on your own Docker daemon.

## Requirements

- Docker with Compose v2: Docker Desktop or OrbStack.
- The agent image on the daemon. From the repository root:

      docker build -t autonomous-agents/agent:dev image/

  Or pull a published tag and tag it as `autonomous-agents/agent:dev`.
- For claude Runs, `CLAUDE_CODE_OAUTH_TOKEN` in your shell (from
  `claude setup-token`). For codex Runs, `CODEX_API_KEY`. Without a
  credential a Run still completes; the CLI's authentication failure is
  recorded in its Journal.

## Start

    docker compose -f sandbox/compose.yaml up -d --build

## Start a Run

    curl -u sandbox:sandbox -X POST localhost:8081/agents/hello/run
    curl -u sandbox:sandbox -X POST localhost:8081/agents/sleepy/run

## Look

- Control plane log: `docker compose -f sandbox/compose.yaml logs -f control-plane`
- Journals: the MinIO console at <http://localhost:9001> (user `sandbox`,
  password `sandbox123`), bucket `agentruns`. Or from the shell:

      curl --aws-sigv4 aws:amz:auto:s3 -u sandbox:sandbox123 'http://localhost:9000/agentruns?list-type=2'
      curl --aws-sigv4 aws:amz:auto:s3 -u sandbox:sandbox123 http://localhost:9000/agentruns/hello/<run-id>/meta.json

- A Run whose Journal did not reach the bucket keeps its container. Find it
  with `docker ps -a` and read it with `docker cp <container>:/run/journal .`

## Agents

The files in `sandbox/agents/` are the Agents. Edit or add one, then restart
the control plane; there is no hot reload:

    docker compose -f sandbox/compose.yaml restart control-plane

## Stop

    docker compose -f sandbox/compose.yaml down -v

## Known limit

Runs reach the control plane and MinIO through `host.docker.internal`.
Docker Desktop and OrbStack give every container that name. On a plain Linux
daemon, Run containers do not get it; put the host's LAN or tailnet address
in `control-plane.yaml` instead.
