#!/bin/sh
# Entrypoint for every Run (SPEC §6). Every awkward line is load-bearing and
# measured; read the list under the script in SPEC §6 before changing one.
set -eu
JOURNAL=/run/journal; mkdir -p "$JOURNAL" /run/workspace
STARTED=$(date -u +%FT%TZ)
HEARTBEAT= LIMIT= AGENT=

# State dirs the agent writes as the unprivileged user; Teardown reads them.
export CODEX_HOME=/run/codex CLAUDE_CONFIG_DIR=/run/claude
mkdir -p "$CODEX_HOME" "$CLAUDE_CONFIG_DIR"
chown agent:agent /run/workspace "$CODEX_HOME" "$CLAUDE_CONFIG_DIR"

api()    { curl -fsS --max-time 10 -H "Authorization: Bearer $RUN_TOKEN" "$@"; }
report() { api --data "$(jq -cn --arg s "$1" --arg m "${2:-}" --argjson c "${3:-null}" \
             '{status:$s,message:$m,exit_code:$c}')" "$CONTROL_PLANE_URL/run/status" || true; }
fail()   { echo "entrypoint: $*" >&2; report failed "$*"; exit 1; }

write_meta() {
  jq -n --arg run "$RUN_ID" --arg agent "$AGENT_NAME" --arg cli "$AGENT_CLI" \
        --argjson rc "$1" --arg started "$STARTED" --arg ended "$(date -u +%FT%TZ)" \
        '{run_id:$run,agent:$agent,cli:$cli,exit_code:$rc,started_at:$started,ended_at:$ended}'
}

teardown() {
  rc=$?; trap - EXIT
  kill "$HEARTBEAT" "$LIMIT" 2>/dev/null || true
  kill -TERM "$AGENT" 2>/dev/null || true

  # BOTH globs: codex compresses rollouts when cold.
  find "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" \
       \( -name '*.jsonl' -o -name '*.jsonl.zst' \) \
       -exec cp {} "$JOURNAL/" \; 2>/dev/null || true

  # Ticket 11 adds push_work here; ticket 05 the Journal upload.
  write_meta "$rc" > "$JOURNAL/meta.json"
  report finished "" "$rc"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 143' TERM INT

# Ticket 10 adds install_egress_rules here.
api "$CONTROL_PLANE_URL/run/payload" -o /run/trigger.json || fail "payload fetch"
# Ticket 11 adds install_skills and clone_repos here.

( while sleep 30; do report running; done ) & HEARTBEAT=$!
( sleep "$WALL_CLOCK_SECONDS"; kill -TERM 1 )  & LIMIT=$!

# build_argv: CLI trivia lives here, not in Go (SPEC §6). Never `claude --bare`.
cd /run/workspace
case "$AGENT_CLI" in
  claude)
    set -- claude -p "$AGENT_PROMPT" --output-format stream-json --verbose \
           --dangerously-skip-permissions
    [ -z "${AGENT_PERSONALITY:-}" ] || set -- "$@" --append-system-prompt "$AGENT_PERSONALITY" ;;
  codex)
    set -- codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox \
           -C /run/workspace
    [ -z "${AGENT_PERSONALITY:-}" ] || set -- "$@" -c "developer_instructions=$AGENT_PERSONALITY" ;;
  *) fail "unknown AGENT_CLI: $AGENT_CLI" ;;
esac
if [ -n "${AGENT_EXTRA_ARGS:-}" ]; then        # one arg per line, passed verbatim
  set -f; IFS='
'; set -- "$@" $AGENT_EXTRA_ARGS; set +f; unset IFS
fi
[ "$AGENT_CLI" != codex ] || set -- "$@" "$AGENT_PROMPT"   # codex takes the prompt last

export HOME=/home/agent
setpriv --reuid agent --regid agent --clear-groups "$@" \
  >"$JOURNAL/stream.jsonl" 2>"$JOURNAL/stderr.log" </dev/null &
AGENT=$!
wait "$AGENT"
