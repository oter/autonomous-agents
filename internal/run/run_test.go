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

// SPEC §9: a per-Run token bucket. Over the limit is refused and counted on
// the Run as a throttle event; the Run is never made terminal by it. Every
// request, refused or not, is a sign of life.
func TestTokenBucketThrottlesAndRecords(t *testing.T) {
	s := run.NewStore()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	s.Add(&run.Run{ID: "r1", Token: "t1", Started: t0})

	for i := range run.ThrottleBurst {
		if !s.Allow("r1", t0) {
			t.Fatalf("request %d of a fresh bucket refused", i+1)
		}
	}
	if s.Allow("r1", t0) {
		t.Fatal("request past the burst allowed")
	}
	if s.Allow("r1", t0.Add(time.Millisecond)) {
		t.Fatal("a second request past the burst allowed")
	}
	r, _ := s.Get("r1")
	if r.Throttled != 2 || !r.LastThrottled.Equal(t0.Add(time.Millisecond)) || !r.Seen.Equal(t0.Add(time.Millisecond)) {
		t.Errorf("run = %+v, want 2 throttle events, last at t0+1ms, seen at t0+1ms", r)
	}
	if r.Terminal {
		t.Error("throttling must never make a Run terminal")
	}

	// One token per second refills.
	if !s.Allow("r1", t0.Add(time.Second)) {
		t.Error("one second later, one token should be back")
	}
	if s.Allow("r1", t0.Add(time.Second)) {
		t.Error("only one token should have refilled")
	}
	if s.Allow("unknown", t0) {
		t.Error("an unknown Run is never allowed")
	}
}

// SPEC §9: three missed heartbeats mark a Run stale, once; any request
// clears it; a terminal Run is never stale.
func TestMarkStaleAfterThreeMissedHeartbeats(t *testing.T) {
	s := run.NewStore()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	s.Add(&run.Run{ID: "r1", Token: "t1", Started: t0})

	if s.MarkStale("r1", t0.Add(run.StaleAfter-time.Second), run.StaleAfter) {
		t.Error("stale before three heartbeats were missed")
	}
	if !s.MarkStale("r1", t0.Add(run.StaleAfter), run.StaleAfter) {
		t.Error("not stale after three missed heartbeats since start")
	}
	if s.MarkStale("r1", t0.Add(run.StaleAfter+time.Second), run.StaleAfter) {
		t.Error("marked stale twice")
	}
	if r, _ := s.Get("r1"); !r.Stale || r.Terminal {
		t.Errorf("run = %+v, want stale and not terminal", r)
	}

	s.Allow("r1", t0.Add(2*run.StaleAfter)) // a heartbeat
	if r, _ := s.Get("r1"); r.Stale {
		t.Error("a request did not clear stale")
	}
	if s.MarkStale("r1", t0.Add(2*run.StaleAfter+time.Second), run.StaleAfter) {
		t.Error("stale right after a heartbeat")
	}
	if !s.MarkStale("r1", t0.Add(3*run.StaleAfter), run.StaleAfter) {
		t.Error("not stale again three heartbeats after the last request")
	}

	s.Add(&run.Run{ID: "r2", Token: "t2", Started: t0})
	s.Finish("r2", 0, run.FromInspect, t0)
	if s.MarkStale("r2", t0.Add(time.Hour), run.StaleAfter) {
		t.Error("a terminal Run was marked stale")
	}
}
