// Tests for dsecrets (SPEC §8) against a fake of the secrets endpoint, run
// with the sh, curl, and jq on PATH; the image's own are exercised by the
// walking skeleton in internal/run. Each measured line of the script has a
// case here that fails if it is undone.
package image_test

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeSecrets is POST /run/secrets as SPEC §9 shapes it: allowed names come
// back as a map, any other name is a 403 naming every denied name. A number
// of 429s with Retry-After can be served first.
type fakeSecrets struct {
	allow    map[string]string
	throttle atomic.Int32
	requests atomic.Int32
}

func (f *fakeSecrets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	if r.Method != "POST" || r.URL.Path != "/run/secrets" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "unauthorized", 401)
		return
	}
	if f.throttle.Add(-1) >= 0 {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "throttled", 429)
		return
	}
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.UnmarshalRead(r.Body, &body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	values, denied := map[string]string{}, []string{}
	for _, n := range body.Names {
		if v, ok := f.allow[n]; ok {
			values[n] = v
		} else {
			denied = append(denied, n)
		}
	}
	if len(denied) > 0 {
		w.WriteHeader(403)
		json.MarshalWrite(w, map[string][]string{"denied": denied})
		return
	}
	json.MarshalWrite(w, values)
}

// dsecrets runs the script against url and returns the child's stdout, the
// stderr, the exit code, and the pid the script was started with.
func dsecrets(t *testing.T, url string, args ...string) (stdout, stderr string, code, pid int) {
	t.Helper()
	for _, tool := range []string{"sh", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	cmd := exec.Command("sh", append([]string{"dsecrets.sh"}, args...)...)
	cmd.Env = append(os.Environ(), "RUN_TOKEN=tok", "CONTROL_PLANE_URL="+url)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return out.String(), errs.String(), cmd.ProcessState.ExitCode(), cmd.Process.Pid
}

const linear = "line one\nline two = x y\n" // newlines, spaces, and = survive

func newFake(t *testing.T) (*fakeSecrets, string) {
	t.Helper()
	f := &fakeSecrets{allow: map[string]string{"LINEAR_API_KEY": linear, "GITHUB_TOKEN": "ghp_ünïcode", "UNASKED": "never"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// The secret name is the environment variable name, the value is byte-exact
// (jq -j plus the sentinel keeps a trailing newline), and a name not asked
// for is not in the child's environment even though the Allowlist has it.
func TestDsecretsExportsExactlyTheNamedValues(t *testing.T) {
	_, url := newFake(t)
	out, errs, code, _ := dsecrets(t, url, "LINEAR_API_KEY,GITHUB_TOKEN", "--", "sh", "-c", `printf '%s|%s|%s' "$LINEAR_API_KEY" "$GITHUB_TOKEN" "${UNASKED:-unset}"`)
	if code != 0 || out != linear+"|ghp_ünïcode|unset" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
	}
}

// SPEC §9: a denied name is a 403 naming the names. dsecrets fails loudly,
// names them, and never runs the child; the same when the control plane
// cannot be reached at all.
func TestDsecretsDeniedIsLoudAndNamed(t *testing.T) {
	_, url := newFake(t)
	out, errs, code, _ := dsecrets(t, url, "LINEAR_API_KEY,AWS_SECRET", "--", "sh", "-c", `echo ran "$LINEAR_API_KEY"`)
	if code != 3 || out != "" || !strings.Contains(errs, "AWS_SECRET") || strings.Contains(errs, "line one") {
		t.Errorf("exit %d, stdout %q, stderr %q; want exit 3 naming AWS_SECRET, no child, no value", code, out, errs)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if out, _, code, _ := dsecrets(t, srv.URL, "LINEAR_API_KEY", "--", "echo", "ran"); code != 3 || out != "" {
		t.Errorf("control plane gone: exit %d, stdout %q; want exit 3 and no child", code, out)
	}
	if _, errs, code, _ := dsecrets(t, url, "LINEAR_API_KEY", "echo", "ran"); code != 2 || !strings.Contains(errs, "--") {
		t.Errorf("without --: exit %d, stderr %q; want exit 2 and the usage", code, errs)
	}
}

// SPEC §8: exec, not fork. The child is the same process the script was,
// so a TERM at the wall clock reaches it and Teardown is never orphaned.
func TestDsecretsExecsTheChild(t *testing.T) {
	_, url := newFake(t)
	out, errs, code, pid := dsecrets(t, url, "GITHUB_TOKEN", "--", "sh", "-c", `echo $$`)
	if got, _ := strconv.Atoi(strings.TrimSpace(out)); code != 0 || got != pid {
		t.Errorf("child pid %q, script pid %d, stderr %q: the child was forked, not exec'd", out, pid, errs)
	}
}

// SPEC §9: a 429 is a wait for Retry-After, never a denial. The request is
// retried and the child runs.
func TestDsecretsWaitsOutAThrottle(t *testing.T) {
	f, url := newFake(t)
	f.throttle.Store(1)
	out, errs, code, _ := dsecrets(t, url, "GITHUB_TOKEN", "--", "sh", "-c", `printf %s "$GITHUB_TOKEN"`)
	if code != 0 || out != "ghp_ünïcode" || f.requests.Load() != 2 {
		t.Errorf("exit %d, stdout %q, stderr %q, %d requests; want success on the second request", code, out, errs, f.requests.Load())
	}
	// A denial after a throttle is still named: the reply is the last line.
	f.throttle.Store(1)
	if out, errs, code, _ := dsecrets(t, url, "AWS_SECRET", "--", "echo", "ran"); code != 3 || out != "" || !strings.Contains(errs, `{"denied":["AWS_SECRET"]}`) {
		t.Errorf("denied after a throttle: exit %d, stdout %q, stderr %q", code, out, errs)
	}
}
