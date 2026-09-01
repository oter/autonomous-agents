package run_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	s.Add(&run.Run{ID: "r1", Token: "good"})
	h := run.API(s, nil)

	for _, tok := range []string{"", "bad"} {
		if res := call(t, h, "GET", "/run/payload", tok, ""); res.StatusCode != 401 {
			t.Errorf("token %q: status %d, want 401", tok, res.StatusCode)
		}
	}
	schemeless := httptest.NewRequest("GET", "/run/payload", nil)
	schemeless.Header.Set("Authorization", "good")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, schemeless)
	if rr.Code != 401 {
		t.Errorf("token without Bearer scheme: status %d, want 401", rr.Code)
	}

	res := call(t, h, "GET", "/run/payload", "good", "")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || strings.TrimSpace(string(body)) != "{}" {
		t.Errorf("payload: %d %q, want 200 {}", res.StatusCode, body)
	}

	if res := call(t, h, "POST", "/run/status", "good", `{"status":"running","message":""}`); res.StatusCode != 204 {
		t.Errorf("status: %d, want 204", res.StatusCode)
	}
	if r, _ := s.Get("r1"); r.Status != "running" || r.Terminal {
		t.Errorf("after running report: %+v", r)
	}

	if res := call(t, h, "POST", "/run/status", "good", `not json`); res.StatusCode != 400 {
		t.Errorf("bad json: %d, want 400", res.StatusCode)
	}

	if res := call(t, h, "POST", "/run/status", "good", `{"status":"finished","message":"","exit_code":143}`); res.StatusCode != 204 {
		t.Errorf("finished: %d, want 204", res.StatusCode)
	}
	if r, _ := s.Get("r1"); !r.Terminal || r.ExitCode != 143 || r.ExitFrom != run.FromReport {
		t.Errorf("after finished report: %+v", r)
	}
}
