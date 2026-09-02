#!/bin/sh
# Entrypoint for every Run (SPEC §6). Every awkward line is load-bearing and
# measured; read the list under the script in SPEC §6 before changing one.
set -eu
JOURNAL=/run/journal; mkdir -p "$JOURNAL" /run/workspace
touch "$JOURNAL/stream.jsonl" "$JOURNAL/stderr.log"   # a Journal has both even if the agent never starts
STARTED=$(date -u +%FT%TZ)
HEARTBEAT= LIMIT= AGENT=
# The at-start facts of meta.json only the control plane knows (SPEC §10).
[ -n "${RUN_META:-}" ] || RUN_META='{}'
# ponytail: one CLI start (about a second) per Run; bake the versions into
# the image at build time if that second ever matters.
CLI_VERSION=$("$AGENT_CLI" --version 2>/dev/null) || CLI_VERSION=

# State dirs the agent writes as the unprivileged user; Teardown reads them.
export CODEX_HOME=/run/codex CLAUDE_CONFIG_DIR=/run/claude
mkdir -p "$CODEX_HOME" "$CLAUDE_CONFIG_DIR"
chown agent:agent /run/workspace "$CODEX_HOME" "$CLAUDE_CONFIG_DIR"

# --retry: a 429 at Teardown must not cost the Journal; curl waits Retry-After.
api()    { curl -fsS --retry 3 --max-time 10 -H "Authorization: Bearer $RUN_TOKEN" "$@"; }
report() { api --data "$(jq -cn --arg s "$1" --arg m "${2:-}" --argjson c "${3:-null}" \
             '{status:$s,message:$m,exit_code:$c}')" "$CONTROL_PLANE_URL/run/status" || true; }
fail()   { echo "entrypoint: $*" >&2; report failed "$*"; exit 1; }

# meta.json (SPEC §10): the control plane's at-start facts, this script's
# own, and a summary read from the event stream rather than from $?.
write_meta() {   # exit code, work push result, journal-urls reply
  stream=$(jq -nR -f /usr/local/share/stream.jq --arg cli "$AGENT_CLI" "$JOURNAL/stream.jsonl" 2>/dev/null) || stream=null
  jq -n --arg run "$RUN_ID" --arg agent "$AGENT_NAME" --arg cli "$AGENT_CLI" --arg cli_version "$CLI_VERSION" \
        --arg prompt "$AGENT_PROMPT" --arg personality "${AGENT_PERSONALITY:-}" --argjson start "$RUN_META" \
        --arg started "$STARTED" --arg ended "$(date -u +%FT%TZ)" --argjson rc "$1" --arg push "$2" \
        --argjson cp "$3" --argjson stream "$stream" \
        '{run_id:$run, agent:$agent, cli:$cli, cli_version:$cli_version, prompt:$prompt, personality:$personality}
         + $start
         + {started_at:$started, ended_at:$ended, duration_seconds:(($ended|fromdate)-($started|fromdate)),
            exit_code:$rc, work_branch:null, work_push:$push, throttle_events:$cp.throttle_events}
         + ($stream // {terminal_reason:"unparsed"})'
}

teardown() {
  rc=$?; trap - EXIT
  # Nothing below may end Teardown before `exit "$rc"`: a failed step is
  # reported, never substituted for the Run's own exit code.
  set +e
  kill "$HEARTBEAT" "$LIMIT" 2>/dev/null
  kill -TERM "$AGENT" 2>/dev/null

  # BOTH globs: codex compresses rollouts when cold. The whole tree, never a
  # date directory: codex names those in local time (SPEC §10).
  find "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" \
       \( -name '*.jsonl' -o -name '*.jsonl.zst' \) \
       -exec cp {} "$JOURNAL/" \; 2>/dev/null

  # Ticket 11 replaces the next line with: push_work && w=pushed || w=failed
  w=none
  # Minted now rather than at spawn, so a long Run cannot outlive them
  # (ADR-0005); the reply also carries the control plane's throttle count.
  urls=$(api "$CONTROL_PLANE_URL/run/journal-urls") || urls='{}'
  # ponytail: the stream is summarised right after the TERM, without waiting
  # for the agent; in the kill path there is no terminal event to wait for,
  # and waiting would spend grace. A bounded wait is the upgrade.
  # A failed write leaves no file: an empty meta.json must not be uploaded.
  write_meta "$rc" "$w" "$urls" > "$JOURNAL/meta.json" || { echo "entrypoint: meta.json not written" >&2; rm -f "$JOURNAL/meta.json"; }
  tar --zstd -cf /run/run.tar.zst -C "$JOURNAL" . || echo "entrypoint: run.tar.zst not written" >&2
  journal="journal upload skipped"
  [ "$urls" = '{}' ] || {
    journal="journal uploaded"   # the control plane removes the container on exactly this message
    curl -fsS -T "$JOURNAL/meta.json" "$(echo "$urls" | jq -r .meta)" || journal="journal upload failed: meta"
    curl -fsS -T /run/run.tar.zst "$(echo "$urls" | jq -r .archive)" || journal="journal upload failed: archive"
  }
  echo "entrypoint: $journal" >&2
  report finished "$journal" "$rc"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 143' TERM INT

# Ticket 10 adds install_egress_rules here.
api "$CONTROL_PLANE_URL/run/payload" -o /run/trigger.json || fail "payload fetch"
# Heartbeat before skills install and clone: those have their own timeouts of
# minutes, and a Run silent that long is marked stale (SPEC §9).
( while sleep 30; do report running; done ) & HEARTBEAT=$!
# Ticket 11 adds install_skills and clone_repos here.

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
