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
	LogLevel string `koanf:"loglevel"`
	Server   struct {
		Port int    `koanf:"port"`
		Host string `koanf:"host"`
	} `koanf:"server"`
	Navidrome struct {
		BaseURL string `koanf:"baseurl"`
		Token   string `koanf:"token"`
	} `koanf:"navidrome"`
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

	return &cfg, nil
}
