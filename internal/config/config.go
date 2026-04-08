package config

import (
	"fmt"
	"os"
	"strings"
	"time"

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
		SearchInterval   string `koanf:"searchinterval"`
	} `koanf:"sync"`
}

const (
	DefaultConfigPath = "config.yaml"
	prefix            = ""
	envPrefix         = "APP_"
)

func Load(configPath string) (*Config, error) {
	k := koanf.New(prefix)

	optionalFile := configPath == ""
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	if err := loadConfigFile(k, configPath, optionalFile); err != nil {
		return nil, fmt.Errorf("failed to load config file %s: %w", configPath, err)
	}

	if err := k.Load(env.Provider(envPrefix, ".", func(s string) string {
		return strings.Replace(
			strings.ToLower(
				strings.TrimPrefix(s, envPrefix),
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
	ApplyDefaults(&cfg)

	return &cfg, nil
}

func loadConfigFile(k *koanf.Koanf, configPath string, optional bool) error {
	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func ApplyDefaults(cfg *Config) {
	if cfg.Sync.Prefer == "" {
		cfg.Sync.Prefer = "local"
	}
	if cfg.Sync.SearchInterval == "" {
		cfg.Sync.SearchInterval = "100ms"
	}
}

func Validate(cfg *Config) error {
	ApplyDefaults(cfg)

	if cfg.Navidrome.BaseURL == "" {
		return fmt.Errorf("navidrome.baseurl is required")
	}
	if cfg.Navidrome.User == "" {
		return fmt.Errorf("navidrome.user is required")
	}
	if cfg.Navidrome.Password == "" {
		return fmt.Errorf("navidrome.password is required")
	}
	prefer := cfg.Sync.Prefer
	if prefer != "local" && prefer != "navidrome" {
		return fmt.Errorf("sync.prefer must be \"local\" or \"navidrome\", got %q", prefer)
	}
	if _, err := ParseSearchInterval(cfg.Sync.SearchInterval); err != nil {
		return fmt.Errorf("sync.searchinterval is invalid: %w", err)
	}
	return nil
}

func ParseSearchInterval(raw string) (time.Duration, error) {
	interval, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	if interval < 0 {
		return 0, fmt.Errorf("must be >= 0")
	}
	return interval, nil
}
