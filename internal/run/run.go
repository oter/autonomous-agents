// Package run starts Runs of Agents and records their outcome (SPEC §5, §9).
package run

import (
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"
)

// HeartbeatInterval is how often the entrypoint reports `running` (SPEC §6).
// StaleAfter is three missed heartbeats (SPEC §9).
const (
	HeartbeatInterval = 30 * time.Second
	StaleAfter        = 3 * HeartbeatInterval
)

// The per-Run token bucket (SPEC §9): ThrottleBurst requests at once, then
// ThrottleRate per second. Generous for a Run that needs a secret per
// command; a shell loop hits it in seconds.
// ponytail: one fixed bucket for every Run; a per-Agent limit in the YAML
// if one Agent ever legitimately needs more.
const (
	ThrottleBurst = 30
	ThrottleRate  = 1.0
)

// NewID allocates a run id in the SPEC §5 format: 20260831-201204-<agent>-<4 hex>.
func NewID(at time.Time, agent string) string {
	var b [2]byte
	rand.Read(b[:])
	return at.UTC().Format("20060102-150405") + "-" + agent + "-" + hex.EncodeToString(b[:])
}

// NewToken mints an opaque RUN_TOKEN: 32 random bytes, base64url (SPEC §9).
func NewToken() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// IsToken reports whether s has the shape NewToken mints. A bearer without
// it is a bad token (401); one with it that no Run holds is an unknown Run
// (404) — after a control-plane restart, an orphan container.
func IsToken(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32
}

// Trigger is what started a Run (CONTEXT.md): a webhook named by its path,
// a schedule by its cron, or the UI's run-now.
type Trigger struct {
	Kind string
	Name string
}

// RunNow is the UI's Trigger (SPEC §5 step 1).
var RunNow = Trigger{Kind: "manual", Name: "run-now"}

// Run is one execution of one Agent (CONTEXT.md).
type Run struct {
	ID        string
	Agent     string
	Runner    string
	Trigger   Trigger
	Container string
	Token     string
	Started   time.Time
	// Secrets is the Agent's Allowlist as it stood at spawn: the names this
	// Run may ask for over the Run API, each mapped to its age ciphertext.
	// The control plane decrypts a value only when asked for it (SPEC §8).
	Secrets map[string]string

	// Seen is the last request over the Run API, on any route, allowed or
	// throttled: every request is a sign of life. Stale is set by the
	// poller after StaleAfter without one, and cleared by the next.
	Seen  time.Time
	Stale bool

	// Last status report from the container over the Run API.
	Status   string
	Message  string
	Reported time.Time

	// Throttle events: how many requests were refused with 429, and when
	// the last one was. Surfaced in the UI and the Journal; never a kill.
	Throttled     int
	LastThrottled time.Time
	tokens        float64
	refilled      time.Time

	// Outcome. Terminal is set by the first terminal state; ExitFrom records
	// who set ExitCode.
	Terminal bool
	Ended    time.Time
	ExitCode int
	ExitFrom ExitSource
}

// ExitSource is who reported an exit code: Docker (authoritative) or the
// container's own finished report.
type ExitSource string

const (
	FromInspect ExitSource = "inspect"
	FromReport  ExitSource = "report"
)

// Store holds every Run the control plane knows about.
// ponytail: in-memory; Runs are forgotten on restart. Persist when a later
// ticket needs history across deploys.
type Store struct {
	mu      sync.Mutex
	byID    map[string]*Run
	byToken map[string]string
}

func NewStore() *Store {
	return &Store{byID: map[string]*Run{}, byToken: map[string]string{}}
}

func (s *Store) Add(r *Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[r.ID] = r
	s.byToken[r.Token] = r.ID
}

// Get returns a copy of the Run.
func (s *Store) Get(id string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return Run{}, false
	}
	return *r, true
}

// ByToken resolves a bearer token to its Run; an empty token never matches.
func (s *Store) ByToken(token string) (Run, bool) {
	if token == "" {
		return Run{}, false
	}
	s.mu.Lock()
	id, ok := s.byToken[token]
	s.mu.Unlock()
	if !ok {
		return Run{}, false
	}
	return s.Get(id)
}

// Allow records a request from the Run at `at` and reports whether it is
// within the Run's token bucket. A refusal is a throttle event, counted on
// the Run; nothing here ends a Run (SPEC §9: throttled, never auto-killed).
func (s *Store) Allow(id string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return false
	}
	r.Seen, r.Stale = at, false
	// A fresh bucket has refilled==zero, so the elapsed time is huge and the
	// bucket starts full.
	r.tokens = min(ThrottleBurst, r.tokens+at.Sub(r.refilled).Seconds()*ThrottleRate)
	r.refilled = at
	if r.tokens < 1 {
		r.Throttled, r.LastThrottled = r.Throttled+1, at
		return false
	}
	r.tokens--
	return true
}

// MarkStale flags a live Run that has made no request for `after` — three
// missed heartbeats (SPEC §9). Returns whether the Run newly became stale,
// which is a hint to ask Docker, never a conclusion. Any request clears it.
func (s *Store) MarkStale(id string, at time.Time, after time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok || r.Terminal || r.Stale || at.Sub(cmp.Or(r.Seen, r.Started)) < after {
		return false
	}
	r.Stale = true
	return true
}

// Report records a status report from the container. A terminal Run keeps
// its last accepted report: the API refuses such requests with 403, and
// this closes the window between that check and the record.
func (s *Store) Report(id, status, message string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.byID[id]; ok && !r.Terminal {
		r.Status, r.Message, r.Reported = status, message, at
	}
}

// Finish records a terminal state. The first terminal state wins; a later
// ContainerInspect still overrides the exit code because Docker is
// authoritative (SPEC §9). Returns whether anything changed.
func (s *Store) Finish(id string, exitCode int, from ExitSource, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return false
	}
	switch {
	case !r.Terminal:
		r.Terminal, r.Ended = true, at
	case from == FromInspect && r.ExitFrom != FromInspect:
	default:
		return false
	}
	r.ExitCode, r.ExitFrom = exitCode, from
	return true
}
