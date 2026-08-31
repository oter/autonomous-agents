# The Journal format

Type: grilling
Status:
Blocked by: 01, 06

## Question

Specify what a Run leaves behind, file by file, inside
`agentruns/<agent>/<run-id>/`.

The stated purpose is analysing behaviour across Runs in order to tune
execution, so the format's real test is: can a future session read a hundred of
these and find a pattern? Design for the reader, not the writer.

- What files exist, and what is in each. At minimum: what the Run was asked to
  do, the trigger payload, the raw event stream from ticket 01, the outcome, and
  enough metadata to correlate with the control plane's own record.
- What metadata is worth capturing at start — Agent name and file hash, Runner,
  trigger kind and source, CLI name and version, limits in effect, skills
  installed and their versions.
- What is worth capturing at the end — exit status, whether a limit was hit,
  duration, turns used, work branch pushed, and any cost or token figures if
  ticket 01 found they are available.
- Is the top-level index (`runs.jsonl`) worth having, and what one line per Run
  needs to contain for it to be useful.
- Concurrency: many Runs across two Runners pushing to one repository will
  collide. Decide the strategy — pull-rebase-retry, one branch per Run, or
  something else — and make sure it cannot lose a Journal.
- What must never appear in a Journal, and who is responsible for keeping it out.
