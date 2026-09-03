package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"golang.org/x/crypto/bcrypt"
)

// The UI listener is private plus basic auth (SPEC §3); run-now sits behind it.
func TestBasicAuth(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	try := func(h http.Handler, user, pass string) int {
		req := httptest.NewRequest("POST", "/agents/a/run", nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	h := basicAuth(ok, "oter", string(hash))
	if c := try(h, "", ""); c != 401 {
		t.Errorf("no creds: %d", c)
	}
	if c := try(h, "oter", "wrong"); c != 401 {
		t.Errorf("wrong password: %d", c)
	}
	if c := try(h, "someone", "hunter2"); c != 401 {
		t.Errorf("wrong user: %d", c)
	}
	if c := try(h, "oter", "hunter2"); c != 200 {
		t.Errorf("right creds: %d", c)
	}
	if c := try(basicAuth(ok, "oter", ""), "oter", ""); c != 401 {
		t.Errorf("empty hash must never authenticate: %d", c)
	}
}

// SPEC §3: the Run API, and with it the secrets endpoint, is served on the
// tailnet-bound run listener and nowhere else. The binary itself is built,
// started on three ephemeral ports with a real 0600 key file, and asked:
// the public hooks listener has nothing at any Run API path, while the run
// listener answers the same requests with the Run API's own 401.
func TestRunAPIOnlyOnTheRunListener(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "control-plane")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("age-master.key", id.String()+"\n", 0o600)
	if err := os.Mkdir(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("agents/a.yaml", "name: a\nagent: claude\nprompt: hi\n", 0o644)
	// Three free ports, released before the binary binds them.
	var ports [3]int
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = l.Addr().(*net.TCPAddr).Port
		l.Close()
	}
	hooks, ui, runAPI := ports[0], ports[1], ports[2]
	write("control-plane.yaml", fmt.Sprintf(`listen: {hooks: "127.0.0.1:%d", ui: "127.0.0.1:%d", run: "127.0.0.1:%d"}
ui: {username: u, password_bcrypt: x}
agents_dir: ./agents
image: autonomous-agents/agent:test
stop_grace: 90s
control_plane_url: http://127.0.0.1:%d
runners: {local: {docker_host: "unix:///nonexistent.sock", max_concurrent: 1}}
secrets: {master_identity: %s}
journal: {endpoint: http://127.0.0.1:1, bucket: b}
`, hooks, ui, runAPI, runAPI, filepath.Join(dir, "age-master.key")), 0o644)

	cmd := exec.Command(bin, "-config", filepath.Join(dir, "control-plane.yaml"))
	cmd.Env = append(os.Environ(), "AWS_ACCESS_KEY_ID=k", "AWS_SECRET_ACCESS_KEY=s")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for _, port := range ports {
		deadline := time.Now().Add(10 * time.Second)
		for {
			c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("port %d never listened:\n%s", port, logs.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	ask := func(port int, method, path string) int {
		t.Helper()
		req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(`{"names":["CLAUDE_CODE_OAUTH_TOKEN"]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer not-a-run-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "/run/secrets"}, {"GET", "/run/payload"}, {"POST", "/run/status"}, {"GET", "/run/journal-urls"},
	} {
		if got := ask(hooks, route.method, route.path); got != 404 {
			t.Errorf("%s %s on the public hooks listener: %d, want 404", route.method, route.path, got)
		}
		if got := ask(runAPI, route.method, route.path); got != 401 {
			t.Errorf("%s %s on the run listener: %d, want the Run API's 401", route.method, route.path, got)
		}
	}
}
