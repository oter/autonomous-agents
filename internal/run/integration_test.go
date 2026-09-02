package run_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/config"
	"github.com/oter/autonomous-agents/internal/docker"
	"github.com/oter/autonomous-agents/internal/run"
)

// fakeBucket is an object store the container PUTs to over a presigned URL
// and the control plane HEADs before removing the container: it keeps
// bodies by path and checks nothing else. The signature arithmetic is
// covered by the AWS worked example in journal_test.go and by the MinIO
// demo in the ticket.
type fakeBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch r.Method {
	case "PUT":
		body, _ := io.ReadAll(r.Body)
		b.objects[r.URL.Path] = body
	default:
		if body, ok := b.objects[r.URL.Path]; ok {
			w.Write(body)
		} else {
			http.NotFound(w, r)
		}
	}
}

func (b *fakeBucket) get(key string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objects["/agentruns/"+key]
	return body, ok
}

// The ticket 03 demo, runnable: a real container from the base image, the
// real entrypoint, the real Run API, and since ticket 05 a Journal that
// lands in a bucket. Needs Docker and the image:
//
//	docker build -t agent-base:dev image/
//	AA_IMAGE=agent-base:dev go test ./internal/run -run WalkingSkeleton -v
//
// Without CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`) the trivial Run
// still completes end to end, with the CLI's auth failure recorded as its
// exit code and stream.
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
	listen := func(t *testing.T, h http.Handler) (*http.Server, int) {
		t.Helper()
		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: h}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Close() })
		return srv, l.Addr().(*net.TCPAddr).Port
	}
	objects := &fakeBucket{objects: map[string][]byte{}}
	_, bucketPort := listen(t, objects)
	bucket := run.Bucket{URL: mustURL(t, fmt.Sprintf("http://host.docker.internal:%d/agentruns", bucketPort)), Region: "auto", AccessKey: "test", SecretKey: "test"}
	_, cpPort := listen(t, run.API(store, bucket, nil))
	sp := &run.Spawner{
		Image:           image,
		StopGrace:       90 * time.Second,
		ControlPlaneURL: fmt.Sprintf("http://host.docker.internal:%d", cpPort),
		Runners:         map[string]*docker.Client{"local": client},
		Store:           store,
		Bucket:          bucket,
		// The bucket URL names the host as containers see it; this process
		// cannot resolve that name, so its HEADs dial the local port instead.
		HTTP: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", bucketPort))
		}}},
		PollInterval: time.Second,
	}
	trigger := run.RunNow
	wait := func(t *testing.T, id, what string, cond func(run.Run) bool) run.Run {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if r, _ := store.Get(id); cond(r) {
				return r
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("run never %s", what)
		return run.Run{}
	}
	// Docker has confirmed the exit: the container's own finished report
	// lands first, and Inspect overrides it a poll later (SPEC §9).
	inspected := func(r run.Run) bool { return r.Terminal && r.ExitFrom == run.FromInspect }
	containerGone := func(t *testing.T, r run.Run) bool {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if exec.Command("docker", "inspect", r.Container).Run() != nil {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}
	parseMeta := func(t *testing.T, b []byte) map[string]any {
		t.Helper()
		var meta map[string]any
		if err := json.Unmarshal(b, &meta); err != nil {
			t.Fatalf("meta.json: %v: %s", err, b)
		}
		return meta
	}
	// The Journal as SPEC §10 lays it out: meta.json and run.tar.zst under
	// <agent>/<run-id>/, the archive holding the stream, stderr, the CLI's
	// own rollout files, and meta.json again.
	journal := func(t *testing.T, r run.Run) map[string]any {
		t.Helper()
		prefix := r.Agent + "/" + r.ID + "/"
		metaBytes, ok := objects.get(prefix + "meta.json")
		if !ok {
			t.Fatalf("bucket has no %smeta.json", prefix)
		}
		archive, ok := objects.get(prefix + "run.tar.zst")
		if !ok {
			t.Fatalf("bucket has no %srun.tar.zst", prefix)
		}
		list := exec.Command("docker", "run", "--rm", "-i", "--entrypoint", "tar", image, "--zstd", "-tf", "-")
		list.Stdin = bytes.NewReader(archive)
		out, err := list.CombinedOutput()
		if err != nil {
			t.Fatalf("listing run.tar.zst: %v: %s", err, out)
		}
		for _, f := range []string{"./stream.jsonl", "./stderr.log", "./meta.json"} {
			if !strings.Contains(string(out), f+"\n") {
				t.Errorf("run.tar.zst is missing %s:\n%s", f, out)
			}
		}
		if strings.Count(string(out), ".jsonl") < 2 {
			t.Errorf("run.tar.zst has no CLI rollout files beside stream.jsonl:\n%s", out)
		}
		if !containerGone(t, r) {
			t.Errorf("container %s still exists although its Journal is in the bucket", r.Container)
		}
		meta := parseMeta(t, metaBytes)
		// The at-start facts (SPEC §10), from both sides of the Run API.
		if meta["agent"] != r.Agent || meta["runner"] != "local" || meta["trigger_kind"] != "manual" ||
			meta["image"] != image || !strings.HasPrefix(meta["image_id"].(string), "sha256:") ||
			meta["cli_version"] == "" || meta["wall_clock_seconds"] == nil || meta["memory"] != "1g" {
			t.Errorf("meta.json start facts = %v", meta)
		}
		if meta["exit_code"] != float64(r.ExitCode) || meta["throttle_events"] != 0.0 || meta["duration_seconds"] == nil || meta["work_push"] != "none" {
			t.Errorf("meta.json end facts = %v", meta)
		}
		return meta
	}
	limits := func(wall time.Duration) config.Limits {
		return config.Limits{WallClock: config.Duration{Duration: wall}, Memory: "1g", CPUs: "1"}
	}

	t.Run("trivial Run completes", func(t *testing.T) {
		started, err := sp.Start(t.Context(), config.Agent{
			Name: "hello", Kind: "claude", Runner: "local", SHA256: "0123abcd",
			Prompt: "Reply with the single word OK.", Limits: limits(5 * time.Minute),
		}, trigger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", started.Container).Run() })
		r := wait(t, started.ID, "reached a terminal state", inspected)
		t.Logf("run %s: exit %d from %s, last report %q %q", r.ID, r.ExitCode, r.ExitFrom, r.Status, r.Message)
		if r.ExitFrom != run.FromInspect || r.Status != "finished" || r.Message != "journal uploaded" {
			t.Errorf("run = %+v, want exit from inspect and a finished report saying the Journal uploaded", r)
		}
		meta := journal(t, r)
		t.Logf("meta.json: %v", meta)
		if meta["agent_sha256"] != "0123abcd" || meta["cli"] != "claude" || meta["prompt"] != "Reply with the single word OK." {
			t.Errorf("meta.json = %v", meta)
		}
		// terminal_reason comes from the stream, not $?: with a credential the
		// result event says completed and carries a dollar cost; without one
		// it is subtype success with is_error true and terminal_reason
		// api_error, the measured trap.
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
			if r.ExitCode != 0 || meta["terminal_reason"] != "completed" || meta["total_cost_usd"] == nil {
				t.Errorf("exit %d, meta = %v; want 0, completed, and a cost", r.ExitCode, meta)
			}
		} else if r.ExitCode != 1 || meta["terminal_reason"] != "api_error" || meta["is_error"] != true {
			t.Errorf("exit %d, meta = %v; want 1, api_error, is_error", r.ExitCode, meta)
		}
	})

	// codex rather than the same Agent: without a credential claude exits in
	// about a second, before any wall clock could fire; codex keeps retrying.
	t.Run("wall clock TERM-kills and still leaves a Journal", func(t *testing.T) {
		started, err := sp.Start(t.Context(), config.Agent{
			Name: "sleepy", Kind: "codex", Runner: "local",
			Prompt: "Run `sleep 120` in the shell, then say done.", Limits: limits(5 * time.Second),
		}, trigger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", started.Container).Run() })
		r := wait(t, started.ID, "reached a terminal state", inspected)
		t.Logf("run %s: exit %d from %s, last report %q %q", r.ID, r.ExitCode, r.ExitFrom, r.Status, r.Message)
		if r.ExitCode != 143 || r.ExitFrom != run.FromInspect || r.Message != "journal uploaded" {
			t.Errorf("run = %+v, want exit 143 from inspect and the Journal uploaded", r)
		}
		meta := journal(t, r)
		t.Logf("meta.json: %v", meta)
		// Killed mid-stream: no terminal event, and codex reports no dollars.
		if meta["terminal_reason"] != "no_terminal_event" || meta["total_cost_usd"] != nil || meta["cli"] != "codex" {
			t.Errorf("meta.json = %v", meta)
		}
	})

	// SPEC §9: if the control plane is unreachable, the Run continues. Its
	// own Run API listener is closed once the payload has been fetched; the
	// wall clock still fires, Teardown still runs and leaves the Journal in
	// the container, and only Docker sees the exit because neither the
	// journal-urls fetch nor the finished report can land. The container is
	// kept: it is the only copy.
	t.Run("a Run that loses the control plane still finishes", func(t *testing.T) {
		gone, gonePort := listen(t, run.API(store, bucket, nil))
		alone := *sp
		alone.ControlPlaneURL = fmt.Sprintf("http://host.docker.internal:%d", gonePort)
		started, err := alone.Start(t.Context(), config.Agent{
			Name: "orphan", Kind: "codex", Runner: "local",
			Prompt: "Run `sleep 120` in the shell, then say done.", Limits: limits(10 * time.Second),
		}, trigger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", started.Container).Run() })
		wait(t, started.ID, "fetched its payload", func(r run.Run) bool { return !r.Seen.IsZero() })
		gone.Close()
		r := wait(t, started.ID, "reached a terminal state", inspected)
		t.Logf("run %s: exit %d from %s, last report %q", r.ID, r.ExitCode, r.ExitFrom, r.Status)
		if r.ExitCode != 143 || r.ExitFrom != run.FromInspect || r.Status == "finished" {
			t.Errorf("run = %+v, want exit 143 seen only by Docker", r)
		}
		if _, ok := objects.get(r.Agent + "/" + r.ID + "/meta.json"); ok {
			t.Error("a Run with no control plane uploaded a Journal: where did it get the URLs?")
		}
		if exec.Command("docker", "inspect", r.Container).Run() != nil {
			t.Fatal("container removed although its Journal never reached the bucket")
		}
		dir := t.TempDir()
		if out, err := exec.Command("docker", "cp", r.Container+":/run/journal", dir).CombinedOutput(); err != nil {
			t.Fatalf("docker cp: %v: %s", err, out)
		}
		b, err := os.ReadFile(filepath.Join(dir, "journal", "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if meta := parseMeta(t, b); meta["exit_code"] != 143.0 || meta["throttle_events"] != nil {
			t.Errorf("meta.json = %v, want exit 143 and no throttle count (the control plane was gone)", meta)
		}
	})
}
