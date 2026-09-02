# Summarise a Run's event stream for meta.json (SPEC §10). The entrypoint
# runs: jq -nR -f stream.jq --arg cli "$AGENT_CLI" "$JOURNAL/stream.jsonl"
#
# Raw lines, each parsed on its own: a Run killed mid-write leaves a torn
# last line, and that line must not cost the whole summary. Only a terminal
# event decides terminal_reason, never $?: a sandbox denial exits 0 and a
# sticky error_seen exits 1 after a completed turn. The two CLIs share no
# field; where one does not report a thing, the field is null.
[inputs | fromjson?] as $ev
| if $cli == "claude" then
    # The result event. Branch on is_error and terminal_reason, never on
    # subtype alone: an auth failure is subtype "success" with is_error true.
    ([$ev[] | select(.type == "result")] | last) as $r
    | {
        terminal_reason: (if $r == null then "no_terminal_event" else $r.terminal_reason end),
        is_error: $r.is_error,
        error: (if $r.is_error == true then $r.result else null end),
        num_turns: $r.num_turns,
        total_cost_usd: $r.total_cost_usd,
        usage: $r.usage,
        permission_denials: (if $r == null then null else ($r.permission_denials | length) end),
        error_events: null,
        failed_items: null
      }
  else
    # Only turn.failed is terminal. Top-level error events carry no
    # will_retry, so five "Reconnecting... n/5" lines look exactly like one
    # fatal error: counted, never used to decide the outcome. An item of
    # type error is a warning and is not counted. codex folds a declined
    # patch into status "failed" itself, so failed_items cannot tell a
    # denied edit from a broken one. Cost is tokens only; codex reports no
    # dollars, and the schema says so rather than pretending.
    [$ev[] | select(.type == "turn.completed" or .type == "turn.failed")] as $turns
    | ($turns | last) as $t
    | {
        terminal_reason: (if $t == null then "no_terminal_event" elif $t.type == "turn.failed" then "failed" else "completed" end),
        is_error: null,
        error: $t.error.message,
        num_turns: ($turns | length),
        total_cost_usd: null,
        usage: (reduce ($ev[] | select(.type == "turn.completed") | .usage) as $u
                  (null; if . == null then $u
                         else . as $acc | ($acc + $u) | with_entries(.value = ($acc[.key] // 0) + ($u[.key] // 0)) end)),
        permission_denials: null,
        error_events: ([$ev[] | select(.type == "error")] | length),
        failed_items: ([$ev[] | select(.type == "item.completed" and .item.status == "failed")] | length)
      }
  end
| . + {events: ($ev | length)}
