package config_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oter/autonomous-agents/internal/config"
)

// writeTree writes a control-plane config plus agent files into a temp dir and
// returns the config path. Agent files are given as name -> YAML body.
func writeTree(t *testing.T, controlPlane string, agents map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range agents {
		if err := os.WriteFile(filepath.Join(dir, "agents", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "control-plane.yaml")
	if err := os.WriteFile(cfgPath, []byte(controlPlane), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

const validControlPlane = `
listen:
  hooks: ":8080"
  ui:    ":8081"
  run:   ":8082"
ui:
  username: oter
  password_bcrypt: "$2a$12$placeholder"
agents_dir: ./agents
image: ghcr.io/oter/autonomous-agents/agent:2026-08-31
stop_grace: 90s
control_plane_url: http://100.64.0.1:8082
runners:
  local:
    docker_host: "unix:///var/run/docker.sock"
    max_concurrent: 6
  macmini:
    docker_host: "unix:///shared/macmini-docker.sock"
    max_concurrent: 2
secrets:
  master_identity: /etc/autonomous-agents/age-master.key
journal:
  endpoint: https://acct.r2.cloudflarestorage.com
  bucket: agentruns
`

const minimalAgent = `
name: linear-triage
agent: claude
prompt: |
  Read /run/trigger.json.
`

func TestAgentDefaults(t *testing.T) {
	cfgPath := writeTree(t, validControlPlane, map[string]string{
		"linear-triage.yaml": minimalAgent,
	})

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.Agents[0]

	if a.Runner != "local" {
		t.Errorf("runner = %q, want local", a.Runner)
	}
	if a.Limits.WallClock.Duration != 30*time.Minute {
		t.Errorf("wall_clock = %v, want 30m", a.Limits.WallClock)
	}
	if a.Limits.Memory != "2g" {
		t.Errorf("memory = %q, want 2g (never unbounded)", a.Limits.Memory)
	}
	if a.MaxConcurrent != 1 {
		t.Errorf("max_concurrent = %d, want 1", a.MaxConcurrent)
	}
	if a.Personality != "" {
		t.Errorf("personality = %q, want empty", a.Personality)
	}
	if len(a.Skills) != 0 || len(a.Repos) != 0 || len(a.Secrets) != 0 || len(a.Triggers) != 0 {
		t.Errorf("skills/repos/secrets/triggers not empty: %+v", a)
	}
}

// loadErr loads a tree with one agent file and returns the error, which must
// be non-nil and name the offending file.
func loadErr(t *testing.T, filename, body string) error {
	t.Helper()
	cfgPath := writeTree(t, validControlPlane, map[string]string{filename: body})
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	if !strings.Contains(err.Error(), filename) {
		t.Errorf("error does not name %s: %v", filename, err)
	}
	return err
}

func TestMissingRequiredFieldFailsStartup(t *testing.T) {
	for _, tc := range []struct{ missing, body string }{
		{"name", "agent: claude\nprompt: hi\n"},
		{"agent", "name: a\nprompt: hi\n"},
		{"prompt", "name: a\nagent: claude\n"},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			err := loadErr(t, "bad.yaml", tc.body)
			if !strings.Contains(err.Error(), tc.missing) {
				t.Errorf("error does not mention %q: %v", tc.missing, err)
			}
		})
	}
}

func TestMalformedFileFailsEntireStartup(t *testing.T) {
	cfgPath := writeTree(t, validControlPlane, map[string]string{
		"good.yaml":   minimalAgent,
		"broken.yaml": "name: [unclosed\nagent: claude\n",
	})
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("Load succeeded despite broken.yaml, want total failure")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error does not name broken.yaml: %v", err)
	}
}

func TestUnknownKeyFailsStartup(t *testing.T) {
	loadErr(t, "typo.yaml", "name: a\nagent: claude\nprompt: hi\npromt_extra: oops\n")
}

func TestUnknownAgentKindFailsStartup(t *testing.T) {
	loadErr(t, "bad.yaml", "name: a\nagent: gemini\nprompt: hi\n")
}

func TestUnknownRunnerFailsStartup(t *testing.T) {
	loadErr(t, "bad.yaml", "name: a\nagent: claude\nprompt: hi\nrunner: raspberrypi\n")
}

func TestDuplicateAgentNamesFailStartup(t *testing.T) {
	cfgPath := writeTree(t, validControlPlane, map[string]string{
		"one.yaml": minimalAgent,
		"two.yaml": minimalAgent, // same name: linear-triage
	})
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("Load succeeded despite duplicate names, want error")
	}
	if !strings.Contains(err.Error(), "linear-triage") {
		t.Errorf("error does not name the duplicate: %v", err)
	}
}

func agentWithAuth(auth string) string {
	return "name: a\nagent: claude\nprompt: hi\ntriggers:\n  - kind: webhook\n    path: /hooks/a\n    auth:\n" + auth
}

func TestTriggerAuthShapes(t *testing.T) {
	bad := map[string]string{
		"hmac missing header":   "      scheme: hmac_sha256\n      encoding: hex\n      secret: xx\n",
		"hmac missing encoding": "      scheme: hmac_sha256\n      header: X-Sig\n      secret: xx\n",
		"hmac bad encoding":     "      scheme: hmac_sha256\n      header: X-Sig\n      encoding: rot13\n      secret: xx\n",
		"hmac missing secret":   "      scheme: hmac_sha256\n      header: X-Sig\n      encoding: hex\n",
		"bearer missing secret": "      scheme: bearer\n",
		"unknown scheme":        "      scheme: mtls\n      secret: xx\n",
	}
	for name, auth := range bad {
		t.Run(name, func(t *testing.T) {
			loadErr(t, "a.yaml", agentWithAuth(auth))
		})
	}

	good := map[string]string{
		"hmac full": "      scheme: hmac_sha256\n      header: X-Sig\n      encoding: hex\n      secret: xx\n",
		"bearer":    "      scheme: bearer\n      secret: xx\n",
		"none":      "      scheme: none\n",
	}
	for name, auth := range good {
		t.Run(name, func(t *testing.T) {
			cfgPath := writeTree(t, validControlPlane, map[string]string{"a.yaml": agentWithAuth(auth)})
			if _, err := config.Load(cfgPath); err != nil {
				t.Errorf("Load: %v", err)
			}
		})
	}
}

// Personality is per-CLI (claude appends, codex replaces); the loader's job
// is to carry it verbatim next to the agent kind, never to flatten or map it.
func TestPersonalityCarriedVerbatim(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			cfgPath := writeTree(t, validControlPlane, map[string]string{
				"a.yaml": "name: a\nagent: " + kind + "\nprompt: hi\npersonality: |\n  You are terse.\nlimits:\n  wall_clock: 15m\n",
			})
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			a := cfg.Agents[0]
			if a.Kind != kind || a.Personality != "You are terse.\n" {
				t.Errorf("kind=%q personality=%q", a.Kind, a.Personality)
			}
			if a.Limits.WallClock.Duration != 15*time.Minute {
				t.Errorf("wall_clock = %v, want 15m", a.Limits.WallClock)
			}
			if a.Limits.Memory != "2g" {
				t.Errorf("partial limits override lost memory default: %q", a.Limits.Memory)
			}
		})
	}
}

// The sample Agent YAMLs left by the spec prototype must load as-is.
func TestPrototypeSampleAgents(t *testing.T) {
	samples, err := filepath.Abs("../../.scratch/v1-spec/prototype/04-agent-yaml/agents")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTree(t, strings.Replace(validControlPlane, "./agents", samples, 1), nil)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byName := map[string]config.Agent{}
	for _, a := range cfg.Agents {
		byName[a.Name] = a
	}
	if len(byName) != 3 {
		t.Fatalf("agents = %v, want 3", len(byName))
	}

	pr := byName["github-pr-review"]
	if pr.Kind != "claude" || pr.MaxConcurrent != 4 || len(pr.Triggers) != 2 {
		t.Errorf("github-pr-review = %+v", pr)
	}
	if pr.Triggers[0].Auth.Scheme != "hmac_sha256" || pr.Triggers[1].Auth.Scheme != "bearer" {
		t.Errorf("github-pr-review trigger schemes = %+v", pr.Triggers)
	}

	deps := byName["nightly-deps"]
	if deps.Kind != "codex" || deps.Runner != "macmini" || deps.Limits.WallClock.Duration != 90*time.Minute {
		t.Errorf("nightly-deps = %+v", deps)
	}
	if len(deps.Triggers) != 1 || deps.Triggers[0].Cron != "0 3 * * *" || deps.Triggers[0].CatchUp {
		t.Errorf("nightly-deps triggers = %+v", deps.Triggers)
	}

	tri := byName["linear-triage"]
	if len(tri.Secrets) != 2 || !strings.Contains(tri.Secrets["ANTHROPIC_API_KEY"], "BEGIN AGE ENCRYPTED FILE") {
		t.Errorf("linear-triage secrets = %v", tri.Secrets)
	}
}

// A missing agents_dir must fail loudly, never start with zero Agents.
func TestMissingAgentsDirFailsStartup(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "control-plane.yaml")
	if err := os.WriteFile(cfgPath, []byte(validControlPlane), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(cfgPath); err == nil {
		t.Fatal("Load succeeded despite missing agents dir, want error")
	}
}

func TestYmlExtensionIsLoaded(t *testing.T) {
	cfgPath := writeTree(t, validControlPlane, map[string]string{
		"linear-triage.yml": minimalAgent,
	})
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "linear-triage" {
		t.Errorf("agents = %+v, want the .yml agent loaded", cfg.Agents)
	}
}

func TestStrayFileInAgentsDirFailsStartup(t *testing.T) {
	loadErr(t, "notes.txt", "not an agent")
}

func TestHiddenFilesIgnored(t *testing.T) {
	cfgPath := writeTree(t, validControlPlane, map[string]string{
		"linear-triage.yaml": minimalAgent,
		".DS_Store":          "\x00macos junk",
	})
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Errorf("agents = %d, want 1 (.DS_Store ignored)", len(cfg.Agents))
	}
}

func TestZeroMaxConcurrentFailsStartup(t *testing.T) {
	err := loadErr(t, "a.yaml", "name: a\nagent: claude\nprompt: hi\nmax_concurrent: 0\n")
	if !strings.Contains(err.Error(), "max_concurrent") {
		t.Errorf("error does not mention max_concurrent: %v", err)
	}
}

func TestLoadValidTree(t *testing.T) {
	cfgPath := writeTree(t, validControlPlane, map[string]string{
		"linear-triage.yaml": minimalAgent,
	})

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Listen.Hooks != ":8080" || cfg.Listen.UI != ":8081" || cfg.Listen.Run != ":8082" {
		t.Errorf("listen = %+v", cfg.Listen)
	}
	if cfg.UI.Username != "oter" || cfg.UI.PasswordBcrypt != "$2a$12$placeholder" {
		t.Errorf("ui = %+v", cfg.UI)
	}
	if cfg.Image != "ghcr.io/oter/autonomous-agents/agent:2026-08-31" {
		t.Errorf("image = %q", cfg.Image)
	}
	if cfg.StopGrace.Duration != 90*time.Second {
		t.Errorf("stop_grace = %v", cfg.StopGrace)
	}
	if cfg.Runners["macmini"].DockerHost != "unix:///shared/macmini-docker.sock" || cfg.Runners["macmini"].MaxConcurrent != 2 {
		t.Errorf("runners.macmini = %+v", cfg.Runners["macmini"])
	}
	if cfg.Secrets.MasterIdentity != "/etc/autonomous-agents/age-master.key" {
		t.Errorf("master_identity = %q", cfg.Secrets.MasterIdentity)
	}
	if cfg.Journal.Endpoint != "https://acct.r2.cloudflarestorage.com" || cfg.Journal.Bucket != "agentruns" {
		t.Errorf("journal = %+v", cfg.Journal)
	}

	if len(cfg.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(cfg.Agents))
	}
	a := cfg.Agents[0]
	if a.Name != "linear-triage" || a.Kind != "claude" || a.Prompt != "Read /run/trigger.json.\n" {
		t.Errorf("agent = %+v", a)
	}
}

// Everything the spawner consumes must be present at startup: a Run reaches
// the control plane over control_plane_url, runs from image, and a zero
// stop_grace would SIGKILL straight after TERM and lose every Journal.
func TestSpawnConfigRequiredAtStartup(t *testing.T) {
	for _, line := range []string{"control_plane_url: http://100.64.0.1:8082\n", "image: ghcr.io/oter/autonomous-agents/agent:2026-08-31\n", "stop_grace: 90s\n"} {
		key, _, _ := strings.Cut(line, ":")
		cfgPath := writeTree(t, strings.Replace(validControlPlane, line, "", 1), map[string]string{"a.yaml": minimalAgent})
		if _, err := config.Load(cfgPath); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("without %s: err = %v, want it named as required", key, err)
		}
	}
	cfg, err := config.Load(writeTree(t, validControlPlane, map[string]string{"a.yaml": minimalAgent}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlPlaneURL != "http://100.64.0.1:8082" {
		t.Errorf("control_plane_url = %q", cfg.ControlPlaneURL)
	}
}

// memory and cpus are handed to Docker as bytes and nano-CPUs; a value Docker
// would reject must fail startup, not the first Run.
func TestLimitsParse(t *testing.T) {
	loadErr(t, "a.yaml", minimalAgent+"limits:\n  memory: lots\n")
	loadErr(t, "a.yaml", minimalAgent+"limits:\n  cpus: fast\n")
	loadErr(t, "a.yaml", minimalAgent+"limits:\n  wall_clock: 0s\n")

	cfg, err := config.Load(writeTree(t, validControlPlane, map[string]string{
		"a.yaml": minimalAgent + "limits:\n  memory: 512m\n  cpus: \"1.5\"\n",
		"b.yaml": strings.Replace(minimalAgent, "linear-triage", "b", 1),
	}))
	if err != nil {
		t.Fatal(err)
	}
	a, b := cfg.Agents[0], cfg.Agents[1]
	if got, _ := a.Limits.MemoryBytes(); got != 512<<20 {
		t.Errorf("memory = %d, want 512MiB", got)
	}
	if got, _ := a.Limits.NanoCPUs(); got != 1_500_000_000 {
		t.Errorf("nanocpus = %d, want 1.5e9", got)
	}
	if got, _ := b.Limits.MemoryBytes(); got != 2<<30 {
		t.Errorf("default memory = %d, want 2GiB", got)
	}
	if got, _ := b.Limits.NanoCPUs(); got != 0 {
		t.Errorf("default nanocpus = %d, want 0 (unlimited)", got)
	}
}

// SPEC §10: every Journal carries the SHA-256 of the Agent's YAML, so a
// behaviour change can be correlated with a configuration change.
func TestAgentSHA256IsOfTheFileBytes(t *testing.T) {
	cfg, err := config.Load(writeTree(t, validControlPlane, map[string]string{"linear-triage.yaml": minimalAgent}))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(minimalAgent))
	if got := cfg.Agents[0].SHA256; got != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q, want the hash of the file bytes %x", got, sum)
	}
}

// The Journal is what makes a Run durable (SPEC §10): a control plane with
// nowhere to put one must not start. Region defaults to R2's "auto".
func TestJournalRequiredAtStartup(t *testing.T) {
	for _, line := range []string{"  endpoint: https://acct.r2.cloudflarestorage.com\n", "  bucket: agentruns\n"} {
		key := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		cfgPath := writeTree(t, strings.Replace(validControlPlane, line, "", 1), map[string]string{"a.yaml": minimalAgent})
		if _, err := config.Load(cfgPath); err == nil || !strings.Contains(err.Error(), "journal."+key) {
			t.Errorf("without journal.%s: err = %v, want it named as required", key, err)
		}
	}
	cfg, err := config.Load(writeTree(t, validControlPlane, map[string]string{"a.yaml": minimalAgent}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Journal.Region != "auto" {
		t.Errorf("default region = %q, want auto", cfg.Journal.Region)
	}
	cfg, err = config.Load(writeTree(t, validControlPlane+"  region: us-east-1\n", map[string]string{"a.yaml": minimalAgent}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Journal.Region != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", cfg.Journal.Region)
	}
}

// SPEC §2: the secrets map is the Allowlist. A name is the environment
// variable dsecrets exports, so it must be one; a value must be armored age
// ciphertext in a | literal block, because a > folded block joins the armor
// lines and mangles it. Nothing is decrypted at startup: the armor check
// needs no key, and the placeholder ciphertext below is not real.
func TestSecretsValidatedAtStartup(t *testing.T) {
	const armored = "    -----BEGIN AGE ENCRYPTED FILE-----\n    YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+IFgyNTUxOSBQbGFjZWhvbGRlcgo=\n    -----END AGE ENCRYPTED FILE-----\n"
	err := loadErr(t, "a.yaml", minimalAgent+"secrets:\n  my-key: |\n"+armored)
	if !strings.Contains(err.Error(), "secrets.my-key") {
		t.Errorf("error does not name the bad secret: %v", err)
	}
	err = loadErr(t, "a.yaml", minimalAgent+"secrets:\n  LINEAR_API_KEY: >\n"+armored)
	if !strings.Contains(err.Error(), "secrets.LINEAR_API_KEY") || !strings.Contains(err.Error(), "|") {
		t.Errorf("folded block: err = %v, want it named with the | hint", err)
	}
	// A value pasted in plaintext is refused, and the refusal does not
	// repeat it: age's own armor error quotes the offending line.
	if err := loadErr(t, "a.yaml", minimalAgent+"secrets:\n  LINEAR_API_KEY: lin_api_plaintext\n"); strings.Contains(err.Error(), "lin_api_plaintext") {
		t.Errorf("the plaintext value is in the startup error: %v", err)
	}

	cfg, err := config.Load(writeTree(t, validControlPlane, map[string]string{"a.yaml": minimalAgent + "secrets:\n  LINEAR_API_KEY: |\n" + armored}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agents[0].Secrets["LINEAR_API_KEY"]; !strings.HasPrefix(got, "-----BEGIN AGE ENCRYPTED FILE-----\n") || !strings.HasSuffix(got, "-----END AGE ENCRYPTED FILE-----\n") {
		t.Errorf("ciphertext = %q, want the armor verbatim", got)
	}
}

// SPEC §3: the master identity is what turns an Allowlist into values; a
// control plane without one cannot authenticate a single CLI.
func TestMasterIdentityRequiredAtStartup(t *testing.T) {
	cfgPath := writeTree(t, strings.Replace(validControlPlane, "  master_identity: /etc/autonomous-agents/age-master.key\n", "", 1), map[string]string{"a.yaml": minimalAgent})
	if _, err := config.Load(cfgPath); err == nil || !strings.Contains(err.Error(), "secrets.master_identity") {
		t.Errorf("without secrets.master_identity: err = %v, want it named as required", err)
	}
}
