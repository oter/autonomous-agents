# Context

Glossary for the Autonomous Agents project. Terms only — no implementation
detail, no decisions. Decisions live in `docs/adr/`, open questions live in
`.scratch/`.

## Agent

A definition, written as a single YAML file in this repo. Static, named, and
version-controlled. An Agent never executes; it describes what a **Run** of it
should be: its limits, its allowlist, its personality, its skills, its
triggers, and which Runner it belongs on.

One Agent, one YAML file.

## Run

One execution of one Agent. Has an id, a start and end time, an exit status,
and exactly one container. A Run is ephemeral: its filesystem does not survive
it, only its **Journal** does.

Distinguish carefully from Agent. "Spawning an agent" is ambiguous and should
be read as "starting a Run of an Agent".

## Runner

A Docker host on which Runs execute. There are two: the machine the control
plane itself runs on, and a remote one reached over SSH. An Agent names the
Runner it belongs on; a Runner holds the key material for the secrets its Runs
are allowed to decrypt.

## Trigger

Whatever causes a Run to start. Two kinds: an inbound webhook, and a schedule
tick. A Trigger carries a **Payload** — for a webhook, the request body; for a
schedule, nothing.

## Broker

The service that decrypts secrets on demand. It is the only thing that holds
key material, and it never runs inside a Run's container.

## Allowlist

The set of secret names a given Agent's Runs are permitted to decrypt, declared
in the Agent's YAML. The Broker refuses anything outside it.

## Journal

The durable record of a Run: what it was asked to do, what it did, and what it
concluded. Journals for every Agent accumulate in one private repository so
that behaviour can be analysed across Runs.

## Teardown

The phase of a Run after the agent process exits, by any means including being
killed at its time limit. Teardown is what makes a Run's output durable. A Run
that skipped teardown left no trace.

## Skill

A unit of instruction installed into a Run's container before the agent process
starts. An Agent lists the Skills its Runs need.
