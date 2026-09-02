// Tests for stream.jq, the entrypoint's event-stream summary for meta.json
// (SPEC §10). The fixtures are built from the shapes measured in
// .scratch/v1-spec/research/01-headless-claude-and-codex.md, trimmed to the
// fields the filter reads and composed where the doc describes a case in
// prose, so every parser note in SPEC §10 has a line here that would fail
// if it were ignored. Needs jq on PATH, as the image has.
package image_test

import (
	"encoding/json/v2"
	"os/exec"
	"strings"
	"testing"
)

func summarise(t *testing.T, cli, stream string) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH")
	}
	cmd := exec.Command("jq", "-nR", "-f", "stream.jq", "--arg", "cli", cli)
	cmd.Stdin = strings.NewReader(stream)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq: %v: %s", err, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("summary is not one JSON object: %v: %s", err, out)
	}
	return m
}

const claudeInit = `{"type":"system","subtype":"init","cwd":"/run/workspace","session_id":"eda61bcf","model":"claude-opus-5[1m]","permissionMode":"bypassPermissions","slash_commands":["code-review"]}` + "\n"
const claudeTurn = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"cat test.txt"}}],"usage":{"input_tokens":2,"output_tokens":2}},"session_id":"eda61bcf"}` + "\n" +
	`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"ok","is_error":false}]},"session_id":"eda61bcf"}` + "\n"

func TestClaudeSuccess(t *testing.T) {
	result := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"stop_reason":"end_turn","terminal_reason":"completed","total_cost_usd":0.1699985,"usage":{"input_tokens":2,"cache_creation_input_tokens":16395,"cache_read_input_tokens":10027,"output_tokens":41},"modelUsage":{},"permission_denials":[],"result":"OK","session_id":"eda61bcf","duration_ms":2398}` + "\n"
	m := summarise(t, "claude", claudeInit+claudeTurn+result)
	want := map[string]any{
		"terminal_reason": "completed", "is_error": false, "error": nil, "num_turns": 1.0,
		"total_cost_usd": 0.1699985, "permission_denials": 0.0, "error_events": nil, "failed_items": nil, "events": 4.0,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v (%T), want %v", k, m[k], m[k], v)
		}
	}
	if m["usage"].(map[string]any)["output_tokens"] != 41.0 {
		t.Errorf("usage = %v, want claude's own usage object verbatim", m["usage"])
	}
}

// The measured trap: an auth failure is subtype "success" with is_error true
// and terminal_reason api_error. A parser branching on subtype calls it a win.
func TestClaudeAPIErrorIsNotSuccess(t *testing.T) {
	result := `{"type":"result","subtype":"success","is_error":true,"num_turns":1,"stop_reason":"stop_sequence","terminal_reason":"api_error","result":"Not logged in · Please run /login","errors":null,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0},"permission_denials":[]}` + "\n"
	m := summarise(t, "claude", claudeInit+result)
	if m["terminal_reason"] != "api_error" || m["is_error"] != true || m["error"] != "Not logged in · Please run /login" {
		t.Errorf("summary = %v", m)
	}
}

// Sandbox denials exit 0 (SPEC §10): the count is in the result event.
func TestClaudePermissionDenialsCounted(t *testing.T) {
	result := `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"terminal_reason":"completed","total_cost_usd":0.02,"usage":{},"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}},{"tool_name":"Write","tool_input":{}}],"result":"done"}` + "\n"
	if m := summarise(t, "claude", claudeInit+result); m["permission_denials"] != 2.0 {
		t.Errorf("permission_denials = %v, want 2", m["permission_denials"])
	}
}

// Killed at the wall clock: no result event, and the last line is cut
// mid-write. The summary must still come out, saying there was no terminal
// event, with the cost fields null rather than invented.
func TestClaudeKilledMidStream(t *testing.T) {
	m := summarise(t, "claude", claudeInit+claudeTurn+`{"type":"assistant","message":{"role":"assis`)
	if m["terminal_reason"] != "no_terminal_event" || m["total_cost_usd"] != nil || m["usage"] != nil || m["is_error"] != nil {
		t.Errorf("summary = %v", m)
	}
	if m["events"] != 3.0 {
		t.Errorf("events = %v, want 3 (the torn line is not an event)", m["events"])
	}
}

const codexRun = `{"type":"thread.started","thread_id":"01a05921"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I'll create hello.txt."}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/sh -lc 'cat hello.txt'","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/sh -lc 'cat hello.txt'","aggregated_output":"hello\n","exit_code":0,"status":"completed"}}
`
const codexDone = `{"type":"turn.completed","usage":{"input_tokens":34172,"cached_input_tokens":27136,"cache_write_input_tokens":0,"output_tokens":163,"reasoning_output_tokens":26}}
`

func TestCodexSuccessIsTokensOnly(t *testing.T) {
	m := summarise(t, "codex", codexRun+codexDone)
	want := map[string]any{
		"terminal_reason": "completed", "is_error": nil, "error": nil, "num_turns": 1.0,
		"total_cost_usd": nil, "permission_denials": nil, "error_events": 0.0, "failed_items": 0.0, "events": 6.0,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v (%T), want %v", k, m[k], m[k], v)
		}
	}
	if u := m["usage"].(map[string]any); u["input_tokens"] != 34172.0 || u["reasoning_output_tokens"] != 26.0 {
		t.Errorf("usage = %v, want codex's own token counts", u)
	}
}

// Retry noise is shape-identical to a fatal error: five "Reconnecting"
// lines and then a completed turn. error_seen made the CLI exit 1, but the
// stream says the turn completed, and the summary must say both.
func TestCodexRetryNoiseIsNotFailure(t *testing.T) {
	noise := strings.Repeat(`{"type":"error","message":"Reconnecting... 2/5 (unexpected status 401 Unauthorized)"}`+"\n", 5)
	m := summarise(t, "codex", `{"type":"thread.started","thread_id":"x"}`+"\n"+noise+`{"type":"turn.started"}`+"\n"+codexDone)
	if m["terminal_reason"] != "completed" || m["error_events"] != 5.0 || m["error"] != nil {
		t.Errorf("summary = %v", m)
	}
}

func TestCodexTurnFailedIsTerminal(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"x"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for x not found. Defaulting to fallback metadata."}}
{"type":"turn.started"}
{"type":"error","message":"shared rollout token budget exhausted"}
{"type":"turn.failed","error":{"message":"shared rollout token budget exhausted"}}
`
	m := summarise(t, "codex", stream)
	if m["terminal_reason"] != "failed" || m["error"] != "shared rollout token budget exhausted" || m["num_turns"] != 1.0 {
		t.Errorf("summary = %v", m)
	}
	if m["error_events"] != 1.0 {
		t.Errorf("error_events = %v, want 1: an item of type error is a warning, not an error event", m["error_events"])
	}
}

// A declined patch is folded into status "failed" by codex itself; the
// count is honest about that (failed_items, not denials), and a failed
// command is counted the same way.
func TestCodexDeclinedFoldsIntoFailed(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"item_0","type":"file_change","changes":[{"path":"/etc/passwd","kind":"update"}],"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_0","type":"file_change","changes":[{"path":"/etc/passwd","kind":"update"}],"status":"failed"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"false","aggregated_output":"","exit_code":1,"status":"failed"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Could not edit that."}}
` + codexDone
	if m := summarise(t, "codex", stream); m["failed_items"] != 2.0 || m["terminal_reason"] != "completed" {
		t.Errorf("summary = %v, want 2 failed items on a completed turn (exit 0)", m)
	}
}

// web_search serialises id twice; the line still parses (last wins) and
// counts as one event.
func TestCodexDuplicateWebSearchID(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"web_search","id":"ws_abc","query":"zstd tar","action":{"type":"search","query":"zstd tar"}}}
` + codexDone
	if m := summarise(t, "codex", stream); m["events"] != 4.0 || m["terminal_reason"] != "completed" {
		t.Errorf("summary = %v", m)
	}
}

// Summed key by key over the union of keys: a field a later turn reports
// and an earlier one did not is kept, not dropped.
func TestCodexUsageSummedAcrossTurns(t *testing.T) {
	second := `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":163,"reasoning_output_tokens":4,"extra_tokens":7}}` + "\n"
	m := summarise(t, "codex", codexRun+codexDone+`{"type":"turn.started"}`+"\n"+second)
	u := m["usage"].(map[string]any)
	if u["output_tokens"] != 326.0 || u["input_tokens"] != 34272.0 || u["reasoning_output_tokens"] != 30.0 || u["extra_tokens"] != 7.0 || m["num_turns"] != 2.0 {
		t.Errorf("usage = %v, num_turns = %v", u, m["num_turns"])
	}
}

func TestCodexKilledMidStream(t *testing.T) {
	m := summarise(t, "codex", codexRun+`{"type":"item.started","item":{"id":"item_2","type":"command_ex`)
	if m["terminal_reason"] != "no_terminal_event" || m["usage"] != nil || m["num_turns"] != 0.0 || m["events"] != 5.0 {
		t.Errorf("summary = %v", m)
	}
}
