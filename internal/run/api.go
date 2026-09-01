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
// its own status; nothing else is routed here.
func API(store *Store, log *slog.Logger) http.Handler {
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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		run, ok := store.ByToken(tok)
		if !bearer || !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r.WithContext(withRun(r.Context(), run)))
	})
}

type runKey struct{}

func withRun(ctx context.Context, r Run) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}
