package run

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	Bucket          Bucket         // where Journals land; looked at before an exited container is removed
	HTTP            *http.Client   // for that look; default journalHTTP
	Identity        MasterIdentity // decrypts the model credential out of the Allowlist at spawn
	Log             *slog.Logger
	PollInterval    time.Duration // default 5s
	StaleAfter      time.Duration // default StaleAfter: three missed heartbeats
}

var journalHTTP = &http.Client{Timeout: 30 * time.Second}

// Start allocates a run id and RUN_TOKEN, creates and starts the container on
// the Agent's Runner, and polls ContainerInspect in the background until the
// container exits. Queueing against max_concurrent lands in ticket 09.
func (s *Spawner) Start(ctx context.Context, a config.Agent, trig Trigger) (Run, error) {
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
	img, err := client.Image(ctx, s.Image)
	if err != nil {
		return Run{}, err
	}

	now := time.Now()
	r := &Run{ID: NewID(now, a.Name), Agent: a.Name, Runner: a.Runner, Trigger: trig, Token: NewToken(), Started: now, Secrets: a.Secrets}
	// The at-start facts of meta.json that only the control plane knows
	// (SPEC §10); the entrypoint merges them in verbatim. The prompt and
	// personality are already in the environment under their own names.
	meta, err := json.Marshal(struct {
		AgentSHA256 string `json:"agent_sha256"`
		Runner      string `json:"runner"`
		TriggerKind string `json:"trigger_kind"`
		TriggerName string `json:"trigger_name"`
		Image       string `json:"image"`
		ImageID     string `json:"image_id"`
		ImageDigest string `json:"image_digest,omitempty"` // absent for a local build
		WallClock   int    `json:"wall_clock_seconds"`
		Memory      string `json:"memory"`
		CPUs        string `json:"cpus,omitempty"` // absent means unlimited
	}{a.SHA256, a.Runner, trig.Kind, trig.Name, s.Image, img.ID, strings.Join(img.RepoDigests[:min(1, len(img.RepoDigests))], ""),
		int(a.Limits.WallClock.Seconds()), a.Limits.Memory, a.Limits.CPUs})
	if err != nil {
		return Run{}, err
	}
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
		"RUN_META=" + string(meta),
	}
	// The accepted hole (SPEC §8): the model credential is plaintext in the
	// agent's own environment because the CLI consumes it, so it is the one
	// Allowlist value decrypted here rather than on demand. Everything else
	// stays behind dsecrets. claude runs on the Claude subscription only
	// (`claude setup-token` → CLAUDE_CODE_OAUTH_TOKEN); ANTHROPIC_API_KEY is
	// not looked for, since the CLI would prefer it and bill API credits.
	// The control plane's own environment is never a source: an Agent that
	// declares no credential runs without one, and its Journal says so.
	credentialVar := map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN", "codex": "CODEX_API_KEY"}[a.Kind]
	if ct, ok := a.Secrets[credentialVar]; ok {
		v, err := s.Identity.Decrypt(ct)
		if err != nil {
			return Run{}, fmt.Errorf("secret %s: %w", credentialVar, err)
		}
		env = append(env, credentialVar+"="+v)
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
// authoritative over the container's own report. A Run silent for three
// heartbeats is marked stale on the tick that notices, and that same tick's
// Inspect is the "immediate" one (SPEC §9): a gap is a hint to ask Docker,
// never a conclusion. Docker saying "running" leaves the Run alive and
// flagged; nothing here kills. An exited container is removed once both
// Journal objects are seen in the bucket, and kept otherwise: then it is the
// only copy, and docker cp is how to read it.
func (s *Spawner) poll(client *docker.Client, r Run) {
	log := cmp.Or(s.Log, slog.Default()).With("run", r.ID)
	staleAfter := cmp.Or(s.StaleAfter, StaleAfter)
	for range time.Tick(cmp.Or(s.PollInterval, 5*time.Second)) {
		stale := s.Store.MarkStale(r.ID, time.Now(), staleAfter)
		st, err := client.Inspect(context.Background(), r.Container)
		if stale {
			log.Warn("run is stale: no request for three heartbeats", "docker_status", st.Status, "err", err)
		}
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
			if !s.journaled(r) {
				got, _ := s.Store.Get(r.ID)
				log.Warn("journal not in the bucket; container kept for docker cp", "last_report", got.Status, "message", got.Message)
			} else if err := client.Remove(context.Background(), r.Container); err != nil {
				log.Warn("remove container", "err", err)
			}
			return
		}
	}
}

// journaled reports whether both of the Run's Journal objects are in the
// bucket. The control plane's own look decides that an exited container may
// go, never the container's finished report: a Run holds its token and can
// report anything (ADR-0004), and this is the only copy at stake.
func (s *Spawner) journaled(r Run) bool {
	for _, key := range []string{"meta.json", "run.tar.zst"} {
		res, err := cmp.Or(s.HTTP, journalHTTP).Head(s.Bucket.Presign("HEAD", r.Agent+"/"+r.ID+"/"+key, time.Now(), time.Minute))
		if err != nil {
			return false
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return false
		}
	}
	return true
}
