// The control plane: one Go binary that reads Agent YAMLs, spawns Runs over
// the Docker API, and serves the Run API (SPEC §1).
package main

import (
	"crypto/subtle"
	"encoding/json/v2"
	"flag"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/oter/autonomous-agents/internal/config"
	"github.com/oter/autonomous-agents/internal/docker"
	"github.com/oter/autonomous-agents/internal/run"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	cfgPath := flag.String("config", "control-plane.yaml", "control-plane config file")
	flag.Parse()
	log := slog.Default()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	runners := map[string]*docker.Client{}
	for name, r := range cfg.Runners {
		if runners[name], err = docker.New(r.DockerHost); err != nil {
			log.Error("runner", "name", name, "err", err)
			os.Exit(1)
		}
	}
	// The bucket credential lives in the environment, never in the config
	// file (SPEC §3); only presigned URLs for it ever reach a Run.
	for _, v := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		if os.Getenv(v) == "" {
			log.Error("journal credential is not set in the environment", "var", v)
			os.Exit(1)
		}
	}
	bucketURL, err := url.Parse(cfg.Journal.Endpoint + "/" + cfg.Journal.Bucket)
	if err != nil {
		log.Error("journal.endpoint", "err", err)
		os.Exit(1)
	}
	bucket := run.Bucket{
		URL: *bucketURL, Region: cfg.Journal.Region,
		AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"), SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}
	store := run.NewStore()
	spawner := &run.Spawner{
		Image:           cfg.Image,
		StopGrace:       cfg.StopGrace.Duration,
		ControlPlaneURL: cfg.ControlPlaneURL,
		Runners:         runners,
		Store:           store,
		Bucket:          bucket,
		Log:             log,
	}
	agents := map[string]config.Agent{}
	for _, a := range cfg.Agents {
		agents[a.Name] = a
	}

	// "Run now" (SPEC §5 step 1) lives on the private UI listener.
	ui := http.NewServeMux()
	ui.HandleFunc("POST /agents/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		a, ok := agents[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		started, err := spawner.Start(r.Context(), a, run.RunNow)
		if err != nil {
			log.Error("run now", "agent", a.Name, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("run started", "run", started.ID, "agent", a.Name, "container", started.Container)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.MarshalWrite(w, map[string]string{"id": started.ID})
	})

	go serve(cfg.Listen.Run, run.API(store, bucket, log), log)
	serve(cfg.Listen.UI, basicAuth(ui, cfg.UI.Username, cfg.UI.PasswordBcrypt), log)
}

func serve(addr string, h http.Handler, log *slog.Logger) {
	log.Info("listening", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("listen", "addr", addr, "err", err)
		os.Exit(1)
	}
}

// basicAuth guards the UI listener with the configured user and bcrypt hash.
// An empty hash never authenticates.
func basicAuth(next http.Handler, user, hash string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
		passOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="autonomous-agents"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
