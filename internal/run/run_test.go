package run_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/run"
)

// SPEC §5: run id is 20260831-201204-<agent>-<4 hex>.
func TestRunIDFormat(t *testing.T) {
	at := time.Date(2026, 8, 31, 20, 12, 4, 0, time.UTC)
	id := run.NewID(at, "linear-triage")
	if !regexp.MustCompile(`^20260831-201204-linear-triage-[0-9a-f]{4}$`).MatchString(id) {
		t.Errorf("id = %q", id)
	}
	if run.NewID(at, "linear-triage") == id {
		t.Error("two ids for the same second collide")
	}
}

// SPEC §9: the token is opaque, 32 random bytes, base64url, stored not a JWT.
func TestTokenIsOpaque(t *testing.T) {
	a, b := run.NewToken(), run.NewToken()
	if a == b {
		t.Error("tokens repeat")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(a) {
		t.Errorf("token = %q, want 43 base64url chars (32 bytes)", a)
	}
}

// SPEC §9: first terminal state wins; ContainerInspect is authoritative on
// disagreement; a duplicate or late report is ignored.
func TestFirstTerminalStateWinsAndInspectIsAuthoritative(t *testing.T) {
	s := run.NewStore()
	s.Add(&run.Run{ID: "r1", Token: "t1"})

	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.Finish("r1", 0, run.FromReport, t0)                     // the container reports exit 0
	s.Finish("r1", 137, run.FromInspect, t0.Add(time.Second)) // Docker says otherwise
	s.Finish("r1", 1, run.FromReport, t0.Add(2*time.Second))  // late duplicate report

	r, _ := s.Get("r1")
	if !r.Terminal || r.ExitCode != 137 || r.ExitFrom != run.FromInspect {
		t.Errorf("run = %+v, want terminal, exit 137 from inspect", r)
	}
	if !r.Ended.Equal(t0) {
		t.Errorf("ended = %v, want the first terminal time %v", r.Ended, t0)
	}

	s.Add(&run.Run{ID: "r2", Token: "t2"})
	s.Finish("r2", 143, run.FromInspect, t0)
	s.Finish("r2", 0, run.FromReport, t0.Add(time.Second))
	r, _ = s.Get("r2")
	if r.ExitCode != 143 || r.ExitFrom != run.FromInspect {
		t.Errorf("run = %+v, want exit 143 from inspect kept", r)
	}
}
