# Headless `claude` and `codex`: what the CLIs actually give us

Type: research
Status:
Blocked by:

## Question

Almost every other ticket assumes something about how the agent CLIs behave when
run non-interactively inside a container. Establish the facts for **both**
`claude` and `codex`, and note where they diverge, because the Agent YAML has to
abstract over both.

- How is a single non-interactive run invoked, and how is the prompt passed —
  argument, stdin, or file?
- Is there a structured event stream (JSONL or similar), what events does it
  emit, and is it complete enough to reconstruct what the run did? This is the
  Journal's raw material.
- What limit flags exist? A turn or step cap, a token cap, a wall-clock cap.
  Charting assumed turns pass straight through to a CLI flag — confirm that
  holds for both, and say what to do if `codex` has no equivalent.
- What are the exit codes, and can a caller distinguish "finished" from "hit its
  limit" from "crashed"?
- What does each CLI write to disk during a run, and where? Session state,
  caches, credentials.
- How is each authenticated in a headless container, and what does that need
  from the secrets path?
- Where does each look for skills on disk, and does that match where
  `skills add -g -a <agent>` puts them?

Record versions — this is the kind of fact that goes stale.
