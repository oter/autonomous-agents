package run_test

import (
	"encoding/json/v2"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/config"
	"github.com/oter/autonomous-agents/internal/docker"
	"github.com/oter/autonomous-agents/internal/run"
)

// The ticket 03 demo, runnable: a real container from the base image, the
// real entrypoint, the real Run API. Needs Docker and the image:
//
//	docker build -t agent-base:dev image/
//	AA_IMAGE=agent-base:dev go test ./internal/run -run WalkingSkeleton -v
//
// Without ANTHROPIC_API_KEY the trivial Run still completes end to end, with
// the CLI's auth failure recorded as its exit code and stream.
func TestWalkingSkeleton(t *testing.T) {
	image := os.Getenv("AA_IMAGE")
	if image == "" {
		t.Skip("set AA_IMAGE to the base image tag to run the walking skeleton")
	}
	client, err := docker.New("unix:///var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	store := run.NewStore()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: run.API(store, nil)}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	sp := &run.Spawner{
		Image:           image,
		StopGrace:       90 * time.Second,
		ControlPlaneURL: fmt.Sprintf("http://host.docker.internal:%d", l.Addr().(*net.TCPAddr).Port),
		Runners:         map[string]*docker.Client{"local": client},
		Store:           store,
		PollInterval:    time.Second,
	}
	// Waits until Docker has confirmed the exit: the container's own finished
	// report lands first, and Inspect overrides it a poll later (SPEC §9).
	wait := func(t *testing.T, id string) run.Run {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if r, _ := store.Get(id); r.Terminal && r.ExitFrom == run.FromInspect {
				return r
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatal("run never reached a terminal state")
		return run.Run{}
	}
	journal := func(t *testing.T, r run.Run) map[string]any {
		t.Helper()
		dir := t.TempDir()
		if out, err := exec.Command("docker", "cp", r.Container+":/run/journal", dir).CombinedOutput(); err != nil {
			t.Fatalf("docker cp: %v: %s", err, out)
		}
		for _, f := range []string{"stream.jsonl", "stderr.log", "meta.json"} {
			if _, err := os.Stat(filepath.Join(dir, "journal", f)); err != nil {
				t.Errorf("journal is missing %s", f)
			}
		}
		// The CLI's own rollout/transcript files, collected by the find in Teardown.
		if rollouts, _ := filepath.Glob(filepath.Join(dir, "journal", "*.jsonl*")); len(rollouts) < 2 {
			t.Errorf("journal has no CLI rollout files beside stream.jsonl: %v", rollouts)
		}
		var meta map[string]any
		b, _ := os.ReadFile(filepath.Join(dir, "journal", "meta.json"))
		if err := json.Unmarshal(b, &meta); err != nil {
			t.Fatalf("meta.json: %v: %s", err, b)
		}
		return meta
	}
	limits := func(wall time.Duration) config.Limits {
		return config.Limits{WallClock: config.Duration{Duration: wall}, Memory: "1g", CPUs: "1"}
	}

	t.Run("trivial Run completes", func(t *testing.T) {
		started, err := sp.Start(t.Context(), config.Agent{
			Name: "hello", Kind: "claude", Runner: "local",
			Prompt: "Reply with the single word OK.", Limits: limits(5 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", started.Container).Run() })
		r := wait(t, started.ID)
		t.Logf("run %s: exit %d from %s, last report %q", r.ID, r.ExitCode, r.ExitFrom, r.Status)
		if r.ExitFrom != run.FromInspect || r.Status != "finished" {
			t.Errorf("run = %+v, want exit from inspect and a finished report", r)
		}
		if os.Getenv("ANTHROPIC_API_KEY") != "" && r.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0 with a credential", r.ExitCode)
		}
		if meta := journal(t, r); meta["exit_code"] != float64(r.ExitCode) || meta["agent"] != "hello" {
			t.Errorf("meta.json = %v", meta)
		}
	})

	// codex rather than the same Agent: without a credential claude exits in
	// about a second, before any wall clock could fire; codex keeps retrying.
	t.Run("wall clock TERM-kills and still leaves a Journal", func(t *testing.T) {
		started, err := sp.Start(t.Context(), config.Agent{
			Name: "sleepy", Kind: "codex", Runner: "local",
			Prompt: "Run `sleep 120` in the shell, then say done.", Limits: limits(5 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", started.Container).Run() })
		r := wait(t, started.ID)
		t.Logf("run %s: exit %d from %s, last report %q", r.ID, r.ExitCode, r.ExitFrom, r.Status)
		if r.ExitCode != 143 || r.ExitFrom != run.FromInspect || r.Status != "finished" {
			t.Errorf("run = %+v, want exit 143 from inspect and a finished report", r)
		}
		if meta := journal(t, r); meta["exit_code"] != 143.0 {
			t.Errorf("meta.json = %v", meta)
		}
	})
}
