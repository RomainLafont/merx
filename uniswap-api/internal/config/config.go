package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	BaseURL           = "https://trade-api.gateway.uniswap.org/v1"
	DefaultConfigPath = "config.yaml"
)

// Config holds runtime configuration loaded from a YAML file.
type Config struct {
	APIKey         string `yaml:"uniswap_api_key"`
	SwapperAddress string `yaml:"swapper_address"`
	BaseURL        string `yaml:"base_url"`
}

// Load reads configuration from a YAML file at the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("uniswap_api_key is required in %s", path)
	}
	if cfg.SwapperAddress == "" {
		return nil, fmt.Errorf("swapper_address is required in %s", path)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = BaseURL
	}

	return &cfg, nil
}
