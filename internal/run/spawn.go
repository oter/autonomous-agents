package run

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oter/autonomous-agents/internal/config"
	"github.com/oter/autonomous-agents/internal/docker"
)

// Spawner starts Runs on Runners (SPEC §5 steps 3–4, 6, 8).
type Spawner struct {
	Image           string
	StopGrace       time.Duration
	ControlPlaneURL string
	Runners         map[string]*docker.Client
	Store           *Store
	Log             *slog.Logger
	PollInterval    time.Duration // default 5s
}

// Start allocates a run id and RUN_TOKEN, creates and starts the container on
// the Agent's Runner, and polls ContainerInspect in the background until the
// container exits. Queueing against max_concurrent lands in ticket 09.
func (s *Spawner) Start(ctx context.Context, a config.Agent) (Run, error) {
	client, ok := s.Runners[a.Runner]
	if !ok {
		return Run{}, fmt.Errorf("runner %q has no Docker client", a.Runner)
	}
	mem, err := a.Limits.MemoryBytes()
	if err != nil {
		return Run{}, err
	}
	cpus, err := a.Limits.NanoCPUs()
	if err != nil {
		return Run{}, err
	}

	now := time.Now()
	r := &Run{ID: NewID(now, a.Name), Agent: a.Name, Runner: a.Runner, Token: NewToken(), Started: now}
	env := []string{
		"RUN_ID=" + r.ID,
		"RUN_TOKEN=" + r.Token,
		"CONTROL_PLANE_URL=" + s.ControlPlaneURL,
		"AGENT_NAME=" + a.Name,
		"AGENT_CLI=" + a.Kind,
		"AGENT_PROMPT=" + a.Prompt,
		"AGENT_PERSONALITY=" + a.Personality,
		// ponytail: one arg per line; an arg containing a newline, or an
		// empty arg, is unsupported.
		"AGENT_EXTRA_ARGS=" + strings.Join(a.ExtraArgs, "\n"),
		"WALL_CLOCK_SECONDS=" + strconv.Itoa(int(a.Limits.WallClock.Seconds())),
	}
	// ponytail: the model credential is passed through from the control
	// plane's own environment until ticket 06 decrypts it from the Allowlist.
	// claude runs on the Claude subscription (`claude setup-token` →
	// CLAUDE_CODE_OAUTH_TOKEN), with an API key as the alternative.
	for _, name := range map[string][]string{
		"claude": {"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
		"codex":  {"CODEX_API_KEY"},
	}[a.Kind] {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}

	r.Container, err = client.Create(ctx, r.ID, docker.ContainerConfig{
		Image:       s.Image,
		Env:         env,
		StopTimeout: int(s.StopGrace.Seconds()),
		// MemorySwap == Memory disables swap: "conservative, never unbounded" (SPEC §2).
		HostConfig: docker.HostConfig{Memory: mem, MemorySwap: mem, NanoCPUs: cpus},
	})
	if err != nil {
		return Run{}, err
	}
	s.Store.Add(r) // before Start, so the container's first API call finds its token
	if err := client.Start(ctx, r.Container); err != nil {
		s.Store.Report(r.ID, "failed", "start: "+err.Error(), time.Now())
		s.Store.Finish(r.ID, -1, FromInspect, time.Now())
		return Run{}, err
	}
	go s.poll(client, *r)
	return *r, nil
}

// poll asks Docker until the container exits, then records the outcome.
// Inspect is the only observer of an unannounced death (ADR-0004) and is
// authoritative over the container's own report.
// ponytail: the exited container is kept so its Journal can be read with
// docker cp; remove it once ticket 05 uploads the Journal.
func (s *Spawner) poll(client *docker.Client, r Run) {
	log := cmp.Or(s.Log, slog.Default()).With("run", r.ID)
	for range time.Tick(cmp.Or(s.PollInterval, 5*time.Second)) {
		st, err := client.Inspect(context.Background(), r.Container)
		switch {
		case errors.Is(err, docker.ErrNotFound):
			log.Error("container vanished before it exited")
			s.Store.Finish(r.ID, -1, FromInspect, time.Now())
			return
		case err != nil:
			log.Warn("inspect failed", "err", err)
			continue
		case st.Exited():
			s.Store.Finish(r.ID, st.ExitCode, FromInspect, time.Now())
			log.Info("run finished", "exit_code", st.ExitCode, "oom_killed", st.OOMKilled, "status", st.Status)
			return
		}
	}
}
