package run

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// API serves the Run API (SPEC §9): the one channel a container reaches the
// control plane through. Read-only about the Run itself, write-only about
// its own status; nothing else is routed here. bucket is where Journals
// land; only presigned URLs for it ever reach a container. identity is what
// decrypts a Run's Allowlist; it never leaves the control plane.
func API(store *Store, bucket Bucket, identity MasterIdentity, log *slog.Logger) http.Handler {
	log = cmp.Or(log, slog.Default())
	mux := http.NewServeMux()

	// Payload: the raw trigger body for a webhook; {} otherwise, so the agent
	// always has a file to parse. Webhooks land in ticket 07.
	mux.HandleFunc("GET /run/payload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}\n"))
	})

	mux.HandleFunc("POST /run/status", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status   string `json:"status"`
			Message  string `json:"message"`
			ExitCode *int   `json:"exit_code"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		run := r.Context().Value(runKey{}).(Run)
		now := time.Now()
		store.Report(run.ID, body.Status, body.Message, now)
		if body.Status == "finished" && body.ExitCode != nil {
			if !store.Finish(run.ID, *body.ExitCode, FromReport, now) {
				log.Warn("late or duplicate finished report ignored", "run", run.ID, "exit_code", *body.ExitCode)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Journal upload (SPEC §10, ADR-0005): two presigned PUTs under
	// <agent>/<run-id>/, minted when Teardown asks so a long Run cannot
	// outlive them, plus the one end-of-Run fact only the control plane
	// holds and meta.json records: the Run's throttle count.
	mux.HandleFunc("GET /run/journal-urls", func(w http.ResponseWriter, r *http.Request) {
		run := r.Context().Value(runKey{}).(Run)
		prefix, now := run.Agent+"/"+run.ID+"/", time.Now()
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, map[string]any{
			"meta":            bucket.Presign("PUT", prefix+"meta.json", now, JournalURLExpiry),
			"archive":         bucket.Presign("PUT", prefix+"run.tar.zst", now, JournalURLExpiry),
			"throttle_events": run.Throttled,
		})
	})

	// Secrets (SPEC §8, ADR-0003): the glossary's Broker is this route. The
	// named values from the Run's Allowlist, decrypted now. Any name outside it is a 403 naming every denied name,
	// never a silent omission, and nothing is decrypted for a denied
	// request. Every grant and denial is logged with the names; a value
	// appears nowhere but the success body.
	mux.HandleFunc("POST /run/secrets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Names []string `json:"names"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		run := r.Context().Value(runKey{}).(Run)
		var denied []string
		for _, n := range body.Names {
			if _, ok := run.Secrets[n]; !ok {
				denied = append(denied, n)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if len(denied) > 0 {
			log.Warn("secrets denied", "run", run.ID, "agent", run.Agent, "denied", denied)
			w.WriteHeader(http.StatusForbidden)
			json.MarshalWrite(w, map[string][]string{"denied": denied})
			return
		}
		values := map[string]string{}
		for _, n := range body.Names {
			v, err := identity.Decrypt(run.Secrets[n])
			if err != nil {
				log.Error("secret decrypt failed", "run", run.ID, "agent", run.Agent, "name", n, "err", err)
				http.Error(w, "cannot decrypt "+n, http.StatusInternalServerError)
				return
			}
			values[n] = v
		}
		log.Info("secrets granted", "run", run.ID, "agent", run.Agent, "names", body.Names)
		json.MarshalWrite(w, values)
	})

	// SPEC §9 status semantics, in order: 401 bad token, 404 unknown Run,
	// 403 terminal Run, 429 throttled. Allow records the request as a sign
	// of life before its bucket decides, so a throttled Run is noisy, never
	// stale.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer || !IsToken(tok) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		run, ok := store.ByToken(tok)
		if !ok {
			// An orphan container from before a restart, or a forged token.
			log.Warn("request with a token no Run holds", "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		if run.Terminal {
			// A zombie process, or something stranger: worth not burying.
			log.Warn("request from a terminal Run", "run", run.ID, "path", r.URL.Path, "exit_code", run.ExitCode, "exit_from", run.ExitFrom)
			http.Error(w, "run is terminal", http.StatusForbidden)
			return
		}
		if !store.Allow(run.ID, time.Now()) {
			if run.Throttled == 0 {
				log.Warn("run throttled", "run", run.ID, "path", r.URL.Path)
			}
			w.Header().Set("Retry-After", "1")
			http.Error(w, "throttled", http.StatusTooManyRequests)
			return
		}
		mux.ServeHTTP(w, r.WithContext(withRun(r.Context(), run)))
	})
}

type runKey struct{}

func withRun(ctx context.Context, r Run) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}
