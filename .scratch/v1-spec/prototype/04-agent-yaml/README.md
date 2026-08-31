# PROTOTYPE — Agent YAML schema

Throwaway. Written to answer ticket 04 by being concrete enough to argue with.
Nothing here is implemented and nothing reads these files.

**The question:** what does an Agent YAML actually contain, and what belongs in
the control-plane config instead?

Three Agents that are deliberately unalike — a webhook-driven triager, a
scheduled maintenance job on the remote Runner, and a high-concurrency PR
reviewer — plus one `control-plane.yaml`. If a field only makes sense for one of
the three, it is probably in the wrong file.

`# ??` marks a genuine open choice, not a placeholder. React to those first.
