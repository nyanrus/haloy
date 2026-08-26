package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haloydev/haloy/internal/constants"
	"github.com/haloydev/haloy/internal/helpers"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"gopkg.in/yaml.v3"
)

type HaloydConfig struct {
	API           HaloydAPIConfig     `json:"api" yaml:"api" toml:"api"`
	HealthMonitor HealthMonitorConfig `json:"health_monitor" yaml:"health_monitor" toml:"health_monitor"`
	Certificates  CertificatesConfig  `json:"certificates" yaml:"certificates" toml:"certificates"`
}

// CertificatesConfig says which certificates haloyd looks after itself.
type CertificatesConfig struct {
	// External lists domains whose certificate is supplied by the operator
	// rather than obtained over ACME. haloyd will not request, renew or
	// overwrite these; the proxy serves whatever <domain>.pem is in the
	// certificate directory, exactly as it does for the ones ACME wrote.
	//
	// This lives here rather than in an app's `domains:` because it is a
	// fact about the machine, not about the deployment: the .pem is put
	// there by whoever runs the server. An app cannot opt itself into a
	// certificate that nobody placed.
	External []string `json:"external,omitempty" yaml:"external,omitempty" toml:"external,omitempty"`
}

// IsExternal reports whether the operator supplies this domain's certificate.
func (c CertificatesConfig) IsExternal(domain string) bool {
	for _, d := range c.External {
		if strings.EqualFold(d, domain) {
			return true
		}
	}
	return false
}

type HaloydAPIConfig struct {
	Domain                       string `json:"domain" yaml:"domain" toml:"domain"`
	MaxTunnels                   int    `json:"maxTunnels,omitempty" yaml:"max_tunnels,omitempty" toml:"max_tunnels,omitempty"`
	MaxTunnelsPerClient          int    `json:"maxTunnelsPerClient,omitempty" yaml:"max_tunnels_per_client,omitempty" toml:"max_tunnels_per_client,omitempty"`
	AllowHostNetworkPortOverride bool   `json:"allowHostNetworkPortOverride,omitempty" yaml:"allow_host_network_port_override,omitempty" toml:"allow_host_network_port_override,omitempty"`
}

// HealthMonitorConfig holds configuration for continuous health monitoring.
type HealthMonitorConfig struct {
	Enabled  *bool  `json:"enabled" yaml:"enabled" toml:"enabled"`    // nil means enabled (default)
	Interval string `json:"interval" yaml:"interval" toml:"interval"` // e.g., "15s"
	Fall     int    `json:"fall" yaml:"fall" toml:"fall"`             // Mark unhealthy after N failures
	Rise     int    `json:"rise" yaml:"rise" toml:"rise"`             // Mark healthy after N successes
	Timeout  string `json:"timeout" yaml:"timeout" toml:"timeout"`    // Per-check timeout, e.g., "5s"
}

// IsEnabled returns whether health monitoring is enabled.
// Defaults to true if not explicitly set.
func (c *HealthMonitorConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true // Enabled by default
	}
	return *c.Enabled
}

// GetInterval parses the interval string and returns the duration.
// Returns the default of 15s if not set or invalid.
func (c *HealthMonitorConfig) GetInterval() time.Duration {
	if c.Interval == "" {
		return 15 * time.Second
	}
	d, err := time.ParseDuration(c.Interval)
	if err != nil {
		return 15 * time.Second
	}
	return d
}

// GetTimeout parses the timeout string and returns the duration.
// Returns the default of 5s if not set or invalid.
func (c *HealthMonitorConfig) GetTimeout() time.Duration {
	if c.Timeout == "" {
		return 5 * time.Second
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

// GetFall returns the fall threshold, defaulting to 3 if not set.
func (c *HealthMonitorConfig) GetFall() int {
	if c.Fall <= 0 {
		return 3
	}
	return c.Fall
}

// GetRise returns the rise threshold, defaulting to 2 if not set.
func (c *HealthMonitorConfig) GetRise() int {
	if c.Rise <= 0 {
		return 2
	}
	return c.Rise
}

// Normalize sets default values for HaloydConfig
func (mc *HaloydConfig) Normalize() *HaloydConfig {
	// Add any defaults if needed in the future
	return mc
}

func (mc *HaloydConfig) Validate() error {
	if mc.API.Domain != "" {
		if err := helpers.IsValidDomain(mc.API.Domain); err != nil {
			return fmt.Errorf("invalid domain format: %w", err)
		}
	}
	if mc.API.MaxTunnels < 0 {
		return fmt.Errorf("api.max_tunnels cannot be negative")
	}
	if mc.API.MaxTunnelsPerClient < 0 {
		return fmt.Errorf("api.max_tunnels_per_client cannot be negative")
	}
	if mc.API.MaxTunnels > 0 && mc.API.MaxTunnelsPerClient > mc.API.MaxTunnels {
		return fmt.Errorf("api.max_tunnels_per_client cannot exceed api.max_tunnels")
	}

	return nil
}

func LoadHaloydConfig(path string) (*HaloydConfig, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	format, err := GetConfigFormat(path)
	if err != nil {
		return nil, err
	}

	parser, err := GetConfigParser(format)
	if err != nil {
		return nil, err
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), parser); err != nil {
		return nil, fmt.Errorf("failed to load haloyd config file: %w", err)
	}

	var haloydConfig HaloydConfig
	if err := k.UnmarshalWithConf("", &haloydConfig, koanf.UnmarshalConf{Tag: format}); err != nil {
		return nil, fmt.Errorf("failed to unmarshal haloyd config: %w", err)
	}
	return &haloydConfig, nil
}

func SaveHaloydConfig(config *HaloydConfig, path string) error {
	ext := filepath.Ext(path)
	var data []byte
	var err error

	switch ext {
	case ".json":
		data, err = json.MarshalIndent(config, "", "  ")
	default: // yaml
		data, err = yaml.Marshal(config)
	}

	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(path, data, constants.ModeFileDefault)
}
