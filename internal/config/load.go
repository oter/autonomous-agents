// Package config loads the control-plane config and every Agent definition
// from agents_dir, per SPEC §2 and §3. Any malformed file fails the whole
// load: configuration comes from reviewed git, and a loudly failed deploy
// beats one Agent silently not running.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	AgentsDir string            `yaml:"agents_dir"`
	Image     string            `yaml:"image"`
	StopGrace Duration          `yaml:"stop_grace"`
	Runners   map[string]Runner `yaml:"runners"`
	Secrets   struct {
		MasterIdentity string `yaml:"master_identity"`
	} `yaml:"secrets"`
	Journal struct {
		Endpoint string `yaml:"endpoint"`
		Bucket   string `yaml:"bucket"`
	} `yaml:"journal"`

	// Agents holds every definition found in AgentsDir, in file-path order.
	Agents []Agent `yaml:"-"`
}

type Runner struct {
	DockerHost    string `yaml:"docker_host"`
	MaxConcurrent int    `yaml:"max_concurrent"`
}

type Agent struct {
	Name string `yaml:"name"`
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
	// Agent's Runs are permitted to decrypt, mapped to their encrypted values.
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
	if err := decodeFile(path, cfg); err != nil {
		return nil, err
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
		if err := decodeFile(p, &a); err != nil {
			return nil, err
		}
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
	for i, tr := range a.Triggers {
		if err := tr.validate(); err != nil {
			return fmt.Errorf("triggers[%d]: %w", i, err)
		}
	}
	return nil
}

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

func decodeFile(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
