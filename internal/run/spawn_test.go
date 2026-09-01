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
	mux.HandleFunc("GET /containers/cid123/json", func(w http.ResponseWriter, r *http.Request) {
		if f.inspects.Add(1) == 1 {
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
	// The subscription token is what claude Runs use; the API key stays as
	// the alternative. Both are copied in when set.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-test")
	t.Setenv("ANTHROPIC_API_KEY", "")
	agent := config.Agent{
		Name: "hello", Kind: "claude", Prompt: "Say OK.", Personality: "Terse.",
		Runner: "local", ExtraArgs: []string{"--max-turns", "3"},
		Limits: config.Limits{WallClock: config.Duration{Duration: 5 * time.Minute}, Memory: "512m", CPUs: "1.5"},
	}

	r, err := sp.Start(t.Context(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.ID, "-hello-") || r.Container != "cid123" || r.Token == "" {
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
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %q in %q", want, env)
		}
	}

	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "ANTHROPIC_API_KEY=") }) {
		t.Errorf("empty credential must not be passed: %q", env)
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
}

func TestStartUnknownRunner(t *testing.T) {
	sp := &run.Spawner{Store: run.NewStore(), Runners: map[string]*docker.Client{}}
	if _, err := sp.Start(t.Context(), config.Agent{Name: "a", Runner: "macmini"}); err == nil {
		t.Fatal("want error for a Runner with no client")
	}
}
