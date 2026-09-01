# 11: Skills, repos, and the work push

**What to build:** An Agent that declares Skills and repos gets a Run where the skills are installed and the repos cloned before the agent starts, and whose Teardown pushes the work branch — so agent work survives the container. Failure to set up is a loud abort, never a silent behavior change.

**Blocked by:** 03 (Walking-skeleton Run), 05 (Journal in object storage), 06 (Secrets end to end).

**Status:** ready-for-agent

- [ ] Skills install project-scoped at container start so BOTH CLIs find them; never `claude --bare` (measured: it yields no skills with no error)
- [ ] Skills install has its own timeout and does not consume the Run's wall clock
- [ ] Payload fetch, skills install, and clone each abort the Run on failure — an Agent whose prompt assumes skills must not proceed without them — and every abort still runs Teardown, leaving a Journal that explains itself
- [ ] Declared repos are cloned to their declared paths; checking out a specific ref is the agent's own job from its trigger payload (no templating)
- [ ] Teardown pushes the work branch FIRST, uploads the Journal LAST, so meta records whether the push succeeded
- [ ] Git credentials for clone/push are resolved without landing in the Journal or the image
- [ ] Demo: an Agent with a skill and a repo commits a change; the branch is on the remote and meta says pushed
