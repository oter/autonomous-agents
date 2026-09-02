package run_test

import (
	"encoding/json/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/config"
	"github.com/oter/autonomous-agents/internal/docker"
	"github.com/oter/autonomous-agents/internal/run"
)

// fakeDocker serves just enough of the Engine API over a unix socket to
// create, start, and inspect one container. The create body is captured.
type fakeDocker struct {
	createQuery string
	create      map[string]any
	inspects    atomic.Int32
	running     atomic.Bool // keep reporting running instead of exiting on the second inspect
	removed     atomic.Bool
}

func newFakeDocker(t *testing.T) (*fakeDocker, string) {
	t.Helper()
	f := &fakeDocker{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		f.createQuery = r.URL.RawQuery
		if err := json.UnmarshalRead(r.Body, &f.create); err != nil {
			t.Error(err)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"Id":"cid123","Warnings":[]}`))
	})
	mux.HandleFunc("POST /containers/cid123/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /images/agent-base:test/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Id":"sha256:deadbeef","RepoTags":["agent-base:test"],"RepoDigests":["ghcr.io/oter/agent-base@sha256:feed"]}`))
	})
	mux.HandleFunc("DELETE /containers/cid123", func(w http.ResponseWriter, r *http.Request) {
		f.removed.Store(true)
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /containers/cid123/json", func(w http.ResponseWriter, r *http.Request) {
		if f.inspects.Add(1) == 1 || f.running.Load() {
			w.Write([]byte(`{"Id":"cid123","State":{"Status":"running","Running":true,"ExitCode":0}}`))
			return
		}
		w.Write([]byte(`{"Id":"cid123","State":{"Status":"exited","Running":false,"ExitCode":3}}`))
	})
	// macOS caps unix socket paths at 104 bytes; t.TempDir() is too long.
	dir, err := os.MkdirTemp("/tmp", "aa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return f, "unix://" + sock
}

// SPEC §5 steps 3–4 and 6: a run id and token are allocated, the container is
// created with the Agent's limits and per-Run environment, and the outcome is
// recorded from ContainerInspect.
func TestStartCreatesContainerAndRecordsInspectOutcome(t *testing.T) {
	fake, host := newFakeDocker(t)
	client, err := docker.New(host)
	if err != nil {
		t.Fatal(err)
	}
	store := run.NewStore()
	sp := &run.Spawner{
		Image:           "agent-base:test",
		StopGrace:       90 * time.Second,
		ControlPlaneURL: "http://cp:8082",
		Runners:         map[string]*docker.Client{"local": client},
		Store:           store,
		PollInterval:    10 * time.Millisecond,
	}
	// claude Runs use the subscription token only. An API key in the control
	// plane's environment must never reach a container: the CLI would prefer
	// it and bill API credits.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-must-not-leak")
	agent := config.Agent{
		Name: "hello", Kind: "claude", Prompt: "Say OK.", Personality: "Terse.", SHA256: "abc123",
		Runner: "local", ExtraArgs: []string{"--max-turns", "3"},
		Limits: config.Limits{WallClock: config.Duration{Duration: 5 * time.Minute}, Memory: "512m", CPUs: "1.5"},
	}

	r, err := sp.Start(t.Context(), agent, run.RunNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.ID, "-hello-") || r.Container != "cid123" || r.Token == "" || r.Trigger.Kind != "manual" {
		t.Errorf("run = %+v", r)
	}
	if fake.createQuery != "name="+r.ID {
		t.Errorf("container name query = %q, want name=%s", fake.createQuery, r.ID)
	}
	if got, _ := store.ByToken(r.Token); got.ID != r.ID {
		t.Error("token is not stored")
	}

	c := fake.create
	if c["Image"] != "agent-base:test" || c["StopTimeout"] != 90.0 {
		t.Errorf("create = %v", c)
	}
	hc := c["HostConfig"].(map[string]any)
	if hc["Memory"] != float64(512<<20) || hc["MemorySwap"] != float64(512<<20) || hc["NanoCpus"] != 1.5e9 {
		t.Errorf("HostConfig = %v", hc)
	}
	var env []string
	for _, e := range c["Env"].([]any) {
		env = append(env, e.(string))
	}
	for _, want := range []string{
		"RUN_ID=" + r.ID, "RUN_TOKEN=" + r.Token, "CONTROL_PLANE_URL=http://cp:8082",
		"AGENT_NAME=hello", "AGENT_CLI=claude", "AGENT_PROMPT=Say OK.", "AGENT_PERSONALITY=Terse.",
		"AGENT_EXTRA_ARGS=--max-turns\n3", "WALL_CLOCK_SECONDS=300",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-test",
		// SPEC §10's at-start facts only the control plane knows, for meta.json.
		`RUN_META={"agent_sha256":"abc123","runner":"local","trigger_kind":"manual","trigger_name":"run-now",` +
			`"image":"agent-base:test","image_id":"sha256:deadbeef","image_digest":"ghcr.io/oter/agent-base@sha256:feed",` +
			`"wall_clock_seconds":300,"memory":"512m","cpus":"1.5"}`,
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %q in %q", want, env)
		}
	}

	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "ANTHROPIC_API_KEY=") }) {
		t.Errorf("ANTHROPIC_API_KEY must never be forwarded to a Run: %q", env)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := store.Get(r.ID)
		if got.Terminal {
			if got.ExitCode != 3 || got.ExitFrom != run.FromInspect || got.Ended.IsZero() {
				t.Errorf("outcome = %+v, want exit 3 from inspect", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never reached a terminal state")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Nothing is in the bucket, so the container is the only copy of the
	// Journal and stays for docker cp.
	time.Sleep(20 * time.Millisecond)
	if fake.removed.Load() {
		t.Error("container removed although its Journal never reached the bucket")
	}
}

// Once both Journal objects are in the bucket, as seen by the control plane
// itself, the exited container has nothing left to offer and is removed. A
// finished report claiming as much is not what decides it.
func TestPollerRemovesContainerOnceJournalIsInTheBucket(t *testing.T) {
	fake, host := newFakeDocker(t)
	fake.running.Store(true)
	client, err := docker.New(host)
	if err != nil {
		t.Fatal(err)
	}
	var heads []string
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heads = append(heads, r.Method+" "+r.URL.Path)
	}))
	t.Cleanup(bucket.Close)
	store := run.NewStore()
	sp := &run.Spawner{
		Image: "agent-base:test", ControlPlaneURL: "http://cp:8082",
		Runners: map[string]*docker.Client{"local": client}, Store: store,
		Bucket:       run.Bucket{URL: mustURL(t, bucket.URL+"/agentruns"), Region: "auto", AccessKey: "k", SecretKey: "s"},
		PollInterval: 5 * time.Millisecond,
	}
	r, err := sp.Start(t.Context(), config.Agent{
		Name: "tidy", Kind: "claude", Runner: "local", Prompt: "x",
		Limits: config.Limits{WallClock: config.Duration{Duration: time.Minute}, Memory: "1g"},
	}, run.RunNow)
	if err != nil {
		t.Fatal(err)
	}
	fake.running.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for !fake.removed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("container was never removed after its Journal was uploaded")
		}
		time.Sleep(time.Millisecond)
	}
	if got, _ := store.Get(r.ID); !got.Terminal || got.ExitFrom != run.FromInspect {
		t.Errorf("run = %+v, want terminal from inspect", got)
	}
	prefix := "HEAD /agentruns/tidy/" + r.ID + "/"
	if len(heads) != 2 || heads[0] != prefix+"meta.json" || heads[1] != prefix+"run.tar.zst" {
		t.Errorf("bucket saw %v, want a HEAD of both objects", heads)
	}
}

func TestStartUnknownRunner(t *testing.T) {
	sp := &run.Spawner{Store: run.NewStore(), Runners: map[string]*docker.Client{}}
	if _, err := sp.Start(t.Context(), config.Agent{Name: "a", Runner: "macmini"}, run.Trigger{}); err == nil {
		t.Fatal("want error for a Runner with no client")
	}
}

// SPEC §9: three missed heartbeats mark the Run stale and the poller asks
// Docker; Docker saying "running" keeps the Run alive and flagged, a
// heartbeat clears the flag, and only Docker's exit ends the Run.
func TestPollerMarksStaleButNeverKills(t *testing.T) {
	fake, host := newFakeDocker(t)
	fake.running.Store(true)
	client, err := docker.New(host)
	if err != nil {
		t.Fatal(err)
	}
	store := run.NewStore()
	sp := &run.Spawner{
		Image: "agent-base:test", ControlPlaneURL: "http://cp:8082",
		Runners: map[string]*docker.Client{"local": client}, Store: store,
		PollInterval: 5 * time.Millisecond, StaleAfter: 40 * time.Millisecond,
	}
	r, err := sp.Start(t.Context(), config.Agent{
		Name: "quiet", Kind: "claude", Runner: "local", Prompt: "x",
		Limits: config.Limits{WallClock: config.Duration{Duration: time.Minute}, Memory: "1g"},
	}, run.RunNow)
	if err != nil {
		t.Fatal(err)
	}
	until := func(what string, cond func(run.Run) bool) run.Run {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if got, _ := store.Get(r.ID); cond(got) {
				return got
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("never %s", what)
		return run.Run{}
	}

	got := until("stale", func(r run.Run) bool { return r.Stale })
	if got.Terminal {
		t.Errorf("a stale Run was ended: %+v", got)
	}
	seen := fake.inspects.Load()
	time.Sleep(30 * time.Millisecond)
	if fake.inspects.Load() <= seen {
		t.Error("the poller stopped asking Docker about a stale Run")
	}

	// A heartbeat over the Run API clears the flag ...
	if res := call(t, run.API(store, run.Bucket{}, nil), "POST", "/run/status", r.Token, `{"status":"running"}`); res.StatusCode != 204 {
		t.Fatalf("heartbeat: %d", res.StatusCode)
	}
	if got, _ := store.Get(r.ID); got.Stale || got.Status != "running" {
		t.Errorf("after a heartbeat: %+v, want not stale", got)
	}
	// ... and silence marks it again.
	until("stale again", func(r run.Run) bool { return r.Stale })

	// Only Docker ends it.
	fake.running.Store(false)
	got = until("terminal", func(r run.Run) bool { return r.Terminal })
	if got.ExitCode != 3 || got.ExitFrom != run.FromInspect {
		t.Errorf("outcome = %+v, want exit 3 from inspect", got)
	}
}
