package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	LogLevel  string `koanf:"loglevel"`
	Navidrome struct {
		BaseURL       string `koanf:"baseurl"`
		User          string `koanf:"user"`
		Password      string `koanf:"password"`
		TLSSkipVerify bool   `koanf:"tlsskipverify"`
	} `koanf:"navidrome"`
	Sync struct {
		MusicPath        string `koanf:"musicpath"`
		Prefer           string `koanf:"prefer"`
		RemotePathPrefix string `koanf:"remotepathprefix"`
	} `koanf:"sync"`
}

const (
	DefaultConfigPath = "config.yaml"
	prefix            = ""
)

var k = koanf.New(prefix)

func Load(configPath string) (*Config, error) {
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("failed to load config file %s: %w", configPath, err)
	}

	if err := k.Load(env.Provider("APP_", ".", func(s string) string {
		return strings.Replace(
			strings.ToLower(
				strings.TrimPrefix(s, "APP_"),
			),
			"_", ".", -1,
		)
	}), nil); err != nil {
		return nil, fmt.Errorf("failed to load env vars: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := Validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func Validate(cfg *Config) error {
	if cfg.Navidrome.BaseURL == "" {
		return fmt.Errorf("navidrome.baseurl is required")
	}
	if cfg.Navidrome.User == "" {
		return fmt.Errorf("navidrome.user is required")
	}
	if cfg.Navidrome.Password == "" {
		return fmt.Errorf("navidrome.password is required")
	}
	if cfg.Sync.Prefer == "" {
		cfg.Sync.Prefer = "local"
	}
	prefer := cfg.Sync.Prefer
	if prefer != "local" && prefer != "navidrome" {
		return fmt.Errorf("sync.prefer must be \"local\" or \"navidrome\", got %q", prefer)
	}
	return nil
}
