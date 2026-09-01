// Package run starts Runs of Agents and records their outcome (SPEC §5, §9).
package run

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"
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

// Run is one execution of one Agent (CONTEXT.md).
type Run struct {
	ID        string
	Agent     string
	Runner    string
	Container string
	Token     string
	Started   time.Time

	// Last status report from the container over the Run API.
	Status   string
	Message  string
	Reported time.Time

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

// Report records a status report from the container.
func (s *Store) Report(id, status, message string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.byID[id]; ok {
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
