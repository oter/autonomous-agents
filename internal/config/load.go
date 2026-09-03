// Package config loads the control-plane config and every Agent definition
// from agents_dir, per SPEC §2 and §3. Any malformed file fails the whole
// load: configuration comes from reviewed git, and a loudly failed deploy
// beats one Agent silently not running.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filippo.io/age/armor"
	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from YAML strings like "30m".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Listen struct {
		Hooks string `yaml:"hooks"`
		UI    string `yaml:"ui"`
		Run   string `yaml:"run"`
	} `yaml:"listen"`
	UI struct {
		Username       string `yaml:"username"`
		PasswordBcrypt string `yaml:"password_bcrypt"`
	} `yaml:"ui"`
	AgentsDir string   `yaml:"agents_dir"`
	Image     string   `yaml:"image"`
	StopGrace Duration `yaml:"stop_grace"`
	// ControlPlaneURL is where a Run reaches the Run API (SPEC §9): the
	// listen.run port as seen from inside a container on any Runner.
	ControlPlaneURL string            `yaml:"control_plane_url"`
	Runners         map[string]Runner `yaml:"runners"`
	Secrets         struct {
		MasterIdentity string `yaml:"master_identity"`
	} `yaml:"secrets"`
	// Journal is the S3-compatible bucket every Journal lands in (SPEC §10).
	// The credential is not here: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY
	// in the control plane's environment, never in a file in git.
	Journal struct {
		Endpoint string `yaml:"endpoint"`
		Bucket   string `yaml:"bucket"`
		// ponytail: an unvalidated string; default auto, which is what R2
		// wants. Validate against the endpoint if a wrong region ever bites.
		Region string `yaml:"region"`
	} `yaml:"journal"`

	// Agents holds every definition found in AgentsDir, in file-path order.
	Agents []Agent `yaml:"-"`
}

type Runner struct {
	DockerHost    string `yaml:"docker_host"`
	MaxConcurrent int    `yaml:"max_concurrent"`
}

type Agent struct {
	// SHA256 is the hex digest of the Agent's file bytes, recorded in every
	// Journal so a behaviour change can be correlated with a configuration
	// change (SPEC §10).
	SHA256 string `yaml:"-"`
	Name   string `yaml:"name"`
	// Kind is the `agent:` key: which CLI runs this Agent, claude or codex.
	Kind   string `yaml:"agent"`
	Prompt string `yaml:"prompt"`
	// Personality is carried verbatim; its meaning is per-CLI (claude
	// appends via --append-system-prompt, codex replaces via
	// -c developer_instructions=) and is resolved at spawn, not here.
	Personality string   `yaml:"personality"`
	Runner      string   `yaml:"runner"`
	Skills      []string `yaml:"skills"`
	Repos       []Repo   `yaml:"repos"`
	// Secrets is the glossary's Allowlist: the set of secret names this
	// Agent's Runs are permitted to decrypt, mapped to their armored age
	// ciphertext. A name is the environment variable dsecrets sets (SPEC §8).
	Secrets       map[string]string `yaml:"secrets"`
	Limits        Limits            `yaml:"limits"`
	ExtraArgs     []string          `yaml:"extra_args"`
	Triggers      []Trigger         `yaml:"triggers"`
	MaxConcurrent int               `yaml:"max_concurrent"`
}

type Repo struct {
	URL  string `yaml:"url"`
	Path string `yaml:"path"`
}

type Limits struct {
	WallClock Duration `yaml:"wall_clock"`
	Memory    string   `yaml:"memory"`
	CPUs      string   `yaml:"cpus"`
}

// MemoryBytes converts the Docker-style memory string ("2g", "512m", "1024k",
// or plain bytes) to bytes.
func (l Limits) MemoryBytes() (int64, error) {
	m := strings.ToLower(l.Memory)
	if m == "" {
		return 0, fmt.Errorf("limits.memory must not be empty")
	}
	mult := int64(1)
	switch m[len(m)-1] {
	case 'k':
		mult, m = 1<<10, m[:len(m)-1]
	case 'm':
		mult, m = 1<<20, m[:len(m)-1]
	case 'g':
		mult, m = 1<<30, m[:len(m)-1]
	}
	n, err := strconv.ParseInt(m, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limits.memory %q: want e.g. 2g or 512m", l.Memory)
	}
	return n * mult, nil
}

// NanoCPUs converts cpus ("1.5") to Docker's NanoCPUs; 0 means unlimited.
func (l Limits) NanoCPUs() (int64, error) {
	if l.CPUs == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(l.CPUs, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("limits.cpus %q: want a positive number", l.CPUs)
	}
	return int64(f * 1e9), nil
}

type Trigger struct {
	Kind     string       `yaml:"kind"`
	Path     string       `yaml:"path"`
	Auth     *TriggerAuth `yaml:"auth"`
	Cron     string       `yaml:"cron"`
	Timezone string       `yaml:"timezone"`
	CatchUp  bool         `yaml:"catch_up"`
}

type TriggerAuth struct {
	Scheme   string `yaml:"scheme"`
	Header   string `yaml:"header"`
	Encoding string `yaml:"encoding"`
	Secret   string `yaml:"secret"`
}

// Load reads the control-plane config at path, then loads and validates every
// Agent YAML in agents_dir. A relative agents_dir is resolved against the
// config file's directory. Any error names the file it came from.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	cfg.Journal.Region = "auto"
	if _, err := decodeFile(path, cfg); err != nil {
		return nil, err
	}
	for key, missing := range map[string]bool{
		"image":                   cfg.Image == "",
		"stop_grace":              cfg.StopGrace.Duration <= 0,
		"control_plane_url":       cfg.ControlPlaneURL == "",
		"journal.endpoint":        cfg.Journal.Endpoint == "",
		"journal.bucket":          cfg.Journal.Bucket == "",
		"secrets.master_identity": cfg.Secrets.MasterIdentity == "",
	} {
		if missing {
			return nil, fmt.Errorf("%s: %s is required", path, key)
		}
	}

	dir := cfg.AgentsDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(path), dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("agents dir: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // hidden files (.DS_Store etc.) must not brick startup
		}
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			return nil, fmt.Errorf("unexpected entry %q in agents dir: one Agent, one .yaml file", name)
		}
		p := filepath.Join(dir, name)
		a := Agent{
			Runner:        "local",
			Limits:        Limits{WallClock: Duration{30 * time.Minute}, Memory: "2g"},
			MaxConcurrent: 1,
		}
		raw, err := decodeFile(p, &a)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		a.SHA256 = hex.EncodeToString(sum[:])
		if err := a.validate(cfg.Runners); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("%s: duplicate agent name %q", p, a.Name)
		}
		seen[a.Name] = true
		cfg.Agents = append(cfg.Agents, a)
	}
	return cfg, nil
}

func (a *Agent) validate(runners map[string]Runner) error {
	if a.Name == "" {
		return fmt.Errorf("name is required")
	}
	if a.Kind == "" {
		return fmt.Errorf("agent is required")
	}
	if a.Kind != "claude" && a.Kind != "codex" {
		return fmt.Errorf("unknown agent %q: must be claude or codex", a.Kind)
	}
	if a.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if _, ok := runners[a.Runner]; !ok {
		return fmt.Errorf("unknown runner %q", a.Runner)
	}
	if a.MaxConcurrent < 1 {
		return fmt.Errorf("max_concurrent must be at least 1, got %d", a.MaxConcurrent)
	}
	if a.Limits.WallClock.Duration <= 0 {
		return fmt.Errorf("limits.wall_clock must be positive, got %v", a.Limits.WallClock)
	}
	if _, err := a.Limits.MemoryBytes(); err != nil {
		return err
	}
	if _, err := a.Limits.NanoCPUs(); err != nil {
		return err
	}
	for name, ct := range a.Secrets {
		if !envName.MatchString(name) {
			return fmt.Errorf("secrets.%s: a secret name must be an environment variable name", name)
		}
		// Only the armor is checked, which needs no key: nothing is decrypted
		// at startup. This is what catches a `>` folded block, which joins
		// the armor lines and mangles it (SPEC §2). The armor error is not
		// repeated: it quotes the offending line, which for a value pasted
		// in plaintext is the value.
		if _, err := io.Copy(io.Discard, armor.NewReader(strings.NewReader(ct))); err != nil {
			return fmt.Errorf("secrets.%s: not armored age ciphertext, use a | literal block", name)
		}
	}
	for i, tr := range a.Triggers {
		if err := tr.validate(); err != nil {
			return fmt.Errorf("triggers[%d]: %w", i, err)
		}
	}
	return nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (tr *Trigger) validate() error {
	switch tr.Kind {
	case "webhook":
		if tr.Path == "" {
			return fmt.Errorf("webhook requires path")
		}
		if tr.Auth == nil {
			return fmt.Errorf("webhook requires auth (use scheme none to opt out)")
		}
		return tr.Auth.validate()
	case "schedule":
		if tr.Cron == "" {
			return fmt.Errorf("schedule requires cron")
		}
		return nil
	default:
		return fmt.Errorf("unknown trigger kind %q: must be webhook or schedule", tr.Kind)
	}
}

func (au *TriggerAuth) validate() error {
	switch au.Scheme {
	case "hmac_sha256":
		if au.Header == "" {
			return fmt.Errorf("hmac_sha256 requires header")
		}
		if au.Encoding != "hex" && au.Encoding != "base64" {
			return fmt.Errorf("hmac_sha256 requires encoding hex or base64, got %q", au.Encoding)
		}
		if au.Secret == "" {
			return fmt.Errorf("hmac_sha256 requires secret")
		}
	case "bearer":
		if au.Secret == "" {
			return fmt.Errorf("bearer requires secret")
		}
	case "none":
	default:
		return fmt.Errorf("unknown auth scheme %q: must be hmac_sha256, bearer, or none", au.Scheme)
	}
	return nil
}

// decodeFile decodes the YAML at path into out and returns the file bytes.
func decodeFile(path string, out any) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return raw, nil
}
