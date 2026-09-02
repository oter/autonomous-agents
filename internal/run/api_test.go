package run_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/run"
)

func call(t *testing.T, h http.Handler, method, path, token, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Result()
}

// SPEC §9: 401 absent/bad token; payload is {} for anything but a webhook;
// status reports are accepted with 204 and a finished report is terminal.
func TestRunAPI(t *testing.T) {
	s := run.NewStore()
	good := run.NewToken()
	s.Add(&run.Run{ID: "r1", Token: good, Started: time.Now()})
	h := run.API(s, nil)

	for _, tok := range []string{"", "bad"} {
		if res := call(t, h, "GET", "/run/payload", tok, ""); res.StatusCode != 401 {
			t.Errorf("token %q: status %d, want 401", tok, res.StatusCode)
		}
	}
	schemeless := httptest.NewRequest("GET", "/run/payload", nil)
	schemeless.Header.Set("Authorization", good)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, schemeless)
	if rr.Code != 401 {
		t.Errorf("token without Bearer scheme: status %d, want 401", rr.Code)
	}

	res := call(t, h, "GET", "/run/payload", good, "")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || strings.TrimSpace(string(body)) != "{}" {
		t.Errorf("payload: %d %q, want 200 {}", res.StatusCode, body)
	}

	if res := call(t, h, "POST", "/run/status", good, `{"status":"running","message":""}`); res.StatusCode != 204 {
		t.Errorf("status: %d, want 204", res.StatusCode)
	}
	if r, _ := s.Get("r1"); r.Status != "running" || r.Terminal {
		t.Errorf("after running report: %+v", r)
	}

	if res := call(t, h, "POST", "/run/status", good, `not json`); res.StatusCode != 400 {
		t.Errorf("bad json: %d, want 400", res.StatusCode)
	}

	if res := call(t, h, "POST", "/run/status", good, `{"status":"finished","message":"","exit_code":143}`); res.StatusCode != 204 {
		t.Errorf("finished: %d, want 204", res.StatusCode)
	}
	if r, _ := s.Get("r1"); !r.Terminal || r.ExitCode != 143 || r.ExitFrom != run.FromReport {
		t.Errorf("after finished report: %+v", r)
	}
}

// SPEC §9: 403 for any request from a Run the control plane considers
// terminal; 404 for a well-formed token no Run holds (an orphan after a
// restart); 429 past the per-Run token bucket, counted on the Run and
// never a kill.
func TestRunAPIStatusSemantics(t *testing.T) {
	s := run.NewStore()
	live, done := run.NewToken(), run.NewToken()
	s.Add(&run.Run{ID: "live", Token: live, Started: time.Now()})
	s.Add(&run.Run{ID: "done", Token: done, Started: time.Now()})
	s.Finish("done", 0, run.FromInspect, time.Now())
	h := run.API(s, nil)

	if res := call(t, h, "GET", "/run/payload", run.NewToken(), ""); res.StatusCode != 404 {
		t.Errorf("unknown Run: %d, want 404", res.StatusCode)
	}
	for _, route := range []struct{ method, path string }{{"GET", "/run/payload"}, {"POST", "/run/status"}} {
		if res := call(t, h, route.method, route.path, done, `{"status":"running"}`); res.StatusCode != 403 {
			t.Errorf("terminal Run on %s %s: %d, want 403", route.method, route.path, res.StatusCode)
		}
	}
	if r, _ := s.Get("done"); r.Status == "running" {
		t.Error("a terminal Run's status report was recorded")
	}

	var got []int
	for range run.ThrottleBurst + 2 {
		got = append(got, call(t, h, "POST", "/run/status", live, `{"status":"running"}`).StatusCode)
	}
	if got[run.ThrottleBurst-1] != 204 || got[run.ThrottleBurst] != 429 || got[run.ThrottleBurst+1] != 429 {
		t.Errorf("statuses = %v, want %d×204 then 429s", got, run.ThrottleBurst)
	}
	res := call(t, h, "GET", "/run/payload", live, "")
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Errorf("payload while throttled: %d %v, want 429 with Retry-After", res.StatusCode, res.Header)
	}
	r, _ := s.Get("live")
	if r.Throttled != 3 || r.LastThrottled.IsZero() || r.Terminal || r.Stale {
		t.Errorf("run = %+v, want 3 throttle events, alive, not stale", r)
	}
}

// SPEC §9's governing principle, enforced in routing: the Run API is
// read-only about the Run itself and write-only about its own status.
// Nothing lists Agents, touches config, or reaches another Run.
func TestRunAPIRoutesNothingElse(t *testing.T) {
	s := run.NewStore()
	s.Add(&run.Run{ID: "r2", Token: run.NewToken(), Started: time.Now()})
	h := run.API(s, nil)
	// A fresh Run per path: probing is throttled like any other request.
	fresh := func(id string) string {
		tok := run.NewToken()
		s.Add(&run.Run{ID: id, Token: tok, Started: time.Now()})
		return tok
	}

	for _, p := range []string{
		"/agents", "/agents/a/run", "/runs", "/runs/r2", "/run/r2/payload",
		"/config", "/run", "/run/secrets/list", "/run/journal", "/",
	} {
		tok := fresh(p)
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			if res := call(t, h, m, p, tok, "{}"); res.StatusCode != 404 {
				t.Errorf("%s %s: %d, want 404", m, p, res.StatusCode)
			}
		}
	}
	// The two routes that exist accept exactly one method each.
	tok := fresh("r1")
	if res := call(t, h, "POST", "/run/payload", tok, "{}"); res.StatusCode != 405 {
		t.Errorf("POST /run/payload: %d, want 405", res.StatusCode)
	}
	if res := call(t, h, "GET", "/run/status", tok, ""); res.StatusCode != 405 {
		t.Errorf("GET /run/status: %d, want 405", res.StatusCode)
	}
}
