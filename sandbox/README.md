# Sandbox

Run the control plane on your own machine, with RustFS as the Journal bucket.
RustFS is an S3-compatible server under the Apache-2.0 license; it stands
where MinIO used to, whose community repository was archived in April 2026.
The control plane runs as a container with the Docker socket, the same way
Coolify runs it. Runs are containers on your own Docker daemon.

## Requirements

- Docker with Compose v2: Docker Desktop or OrbStack.
- The agent image on the daemon. From the repository root:

      docker build -t autonomous-agents/agent:dev image/

  Or pull a published tag and tag it as `autonomous-agents/agent:dev`.
- An age master identity at `sandbox/age-master.key`, which `.gitignore`
  keeps out of the repository. With `age` installed (`brew install age`):

      age-keygen -o sandbox/age-master.key

  Every secret an Agent declares is encrypted to this key, including the
  model credential; see "Secrets" below. Without one a Run still completes,
  and the CLI's authentication failure is recorded in its Journal.

## Start

    docker compose -f sandbox/compose.yaml up -d --build

## Start a Run

    curl -u sandbox:sandbox -X POST localhost:8081/agents/hello/run
    curl -u sandbox:sandbox -X POST localhost:8081/agents/sleepy/run

## Look

- Control plane log: `docker compose -f sandbox/compose.yaml logs -f control-plane`
- Journals: the RustFS console at <http://localhost:9001/rustfs/console/>
  (the access key `sandbox` and secret `sandbox123` are the login), bucket
  `agentruns`. Or from the shell:

      curl --aws-sigv4 aws:amz:auto:s3 -u sandbox:sandbox123 'http://localhost:9000/agentruns?list-type=2'
      curl --aws-sigv4 aws:amz:auto:s3 -u sandbox:sandbox123 http://localhost:9000/agentruns/hello/<run-id>/meta.json

- A Run whose Journal did not reach the bucket keeps its container. Find it
  with `docker ps -a` and read it with `docker cp <container>:/run/journal .`

## Agents

The files in `sandbox/agents/` are the Agents. Edit or add one, then restart
the control plane; there is no hot reload:

    docker compose -f sandbox/compose.yaml restart control-plane

## Secrets

An Agent's `secrets` map is its Allowlist (SPEC §2): each value is age
ciphertext encrypted to the master identity, in a `|` literal block. Encrypt
a value with the key's public half:

    pub=$(age-keygen -y sandbox/age-master.key)
    printf %s "$CLAUDE_CODE_OAUTH_TOKEN" | age -r "$pub" -a

`printf %s`, not `echo`: a trailing newline would become part of the value.
Paste the armor under the secret's name, indented, in the Agent's YAML. The
model credential is one of them: `CLAUDE_CODE_OAUTH_TOKEN` (from
`claude setup-token`) for claude, `CODEX_API_KEY` for codex. It is decrypted
at spawn into the agent's environment; every other value is fetched on
demand with `dsecrets` (SPEC §8), which the model uses from its shell:

    name: secretive
    agent: claude
    prompt: |
      Run `dsecrets DEMO_SECRET -- sh -c 'echo $DEMO_SECRET'` and reply with
      its output. Then run `dsecrets AWS_SECRET -- true` and reply with its
      error message.
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: |
        -----BEGIN AGE ENCRYPTED FILE-----
        ...
        -----END AGE ENCRYPTED FILE-----
      DEMO_SECRET: |
        -----BEGIN AGE ENCRYPTED FILE-----
        ...
        -----END AGE ENCRYPTED FILE-----

The first command gets the value; the second is refused with a 403 that
names `AWS_SECRET`, because it is not in the Allowlist. Each grant and each
denial is a line in the control plane log, with the names and never the
values. The value the model echoed is in the Journal's `stream.jsonl`:
nothing scrubs it (SPEC §10).

## Stop

    docker compose -f sandbox/compose.yaml down -v

## Known limit

Runs reach the control plane and RustFS through `host.docker.internal`.
Docker Desktop and OrbStack give every container that name. On a plain Linux
daemon, Run containers do not get it; put the host's LAN or tailnet address
in `control-plane.yaml` instead.
