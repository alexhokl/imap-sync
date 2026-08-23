package main

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// DestHostConfig holds the destination server connection settings shared by
// every account (host + TLS behaviour). Credentials are per-account (see
// AccountConfig) since each source account is paired with its own dest
// mailbox login on this shared server.
type DestHostConfig struct {
	Host    string
	SkipTLS bool
}

// AccountConfig pairs one source mailbox with its own destination login on
// the shared destination server.
type AccountConfig struct {
	Name string

	SourceHost string
	SourceUser string
	SourcePass string

	DestUser string
	DestPass string

	// StateFile is the path to this account's dedup state file. If empty
	// after parsing, main derives a default from Name.
	StateFile string
}

// AppConfig is the fully-resolved runtime configuration: one shared
// destination host plus one or more source accounts.
type AppConfig struct {
	Dest     DestHostConfig
	Accounts []AccountConfig
	DryRun   bool
}

// yamlConfigFile mirrors the on-disk YAML schema for --config.
type yamlConfigFile struct {
	Dest struct {
		Host    string `yaml:"host"`
		SkipTLS bool   `yaml:"skip_tls"`
	} `yaml:"dest"`
	Accounts []struct {
		Name   string `yaml:"name"`
		Source struct {
			Host string `yaml:"host"`
			User string `yaml:"user"`
			Pass string `yaml:"pass"`
		} `yaml:"source"`
		Dest struct {
			User string `yaml:"user"`
			Pass string `yaml:"pass"`
		} `yaml:"dest"`
		StateFile string `yaml:"state_file"`
	} `yaml:"accounts"`
}

// loadConfigFile reads and validates a multi-account YAML config file.
func loadConfigFile(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied CLI flag, not user input
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var raw yamlConfigFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	cfg := &AppConfig{
		Dest: DestHostConfig{
			Host:    raw.Dest.Host,
			SkipTLS: raw.Dest.SkipTLS,
		},
	}

	if len(raw.Accounts) == 0 {
		return nil, fmt.Errorf("config file must define at least one account")
	}

	seenNames := make(map[string]struct{}, len(raw.Accounts))
	for i, a := range raw.Accounts {
		acc := AccountConfig{
			Name:       a.Name,
			SourceHost: a.Source.Host,
			SourceUser: a.Source.User,
			SourcePass: a.Source.Pass,
			DestUser:   a.Dest.User,
			DestPass:   a.Dest.Pass,
			StateFile:  a.StateFile,
		}
		if _, dup := seenNames[acc.Name]; dup {
			return nil, fmt.Errorf("accounts[%d]: duplicate account name %q", i, acc.Name)
		}
		seenNames[acc.Name] = struct{}{}
		cfg.Accounts = append(cfg.Accounts, acc)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.applyDefaults()
	return cfg, nil
}

// validate checks that all required fields are present.
func (c *AppConfig) validate() error {
	if c.Dest.Host == "" {
		return fmt.Errorf("dest.host is required")
	}
	for i, acc := range c.Accounts {
		label := acc.Name
		if label == "" {
			label = fmt.Sprintf("accounts[%d]", i)
		}
		if acc.Name == "" {
			return fmt.Errorf("accounts[%d]: name is required", i)
		}
		if acc.SourceHost == "" {
			return fmt.Errorf("account %q: source.host is required", label)
		}
		if acc.SourceUser == "" {
			return fmt.Errorf("account %q: source.user is required", label)
		}
		if acc.SourcePass == "" {
			return fmt.Errorf("account %q: source.pass is required", label)
		}
		if acc.DestUser == "" {
			return fmt.Errorf("account %q: dest.user is required", label)
		}
		if acc.DestPass == "" {
			return fmt.Errorf("account %q: dest.pass is required", label)
		}
	}
	return nil
}

// unsafeStateFileChars matches characters not safe to embed directly in a
// derived state-file name.
var unsafeStateFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// applyDefaults fills in derived defaults (currently: per-account state file
// path, when not explicitly set).
func (c *AppConfig) applyDefaults() {
	for i := range c.Accounts {
		if c.Accounts[i].StateFile == "" {
			safe := unsafeStateFileChars.ReplaceAllString(c.Accounts[i].Name, "_")
			c.Accounts[i].StateFile = fmt.Sprintf("./sync-state-%s.json", safe)
		}
	}
}
