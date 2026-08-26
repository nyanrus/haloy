package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/docker/go-units"
	"github.com/go-viper/mapstructure/v2"
	"github.com/haloydev/haloy/internal/helpers"
)

type DeployConfig struct {
	Images           map[string]*Image `json:"images,omitempty" yaml:"images,omitempty" toml:"images,omitempty"`
	TargetConfig     `mapstructure:",squash" json:",inline" yaml:",inline" toml:",inline"`
	Targets          map[string]*TargetConfig `json:"targets,omitempty" yaml:"targets,omitempty" toml:"targets,omitempty"`
	SecretProviders  *SecretProviders         `json:"secretProviders,omitempty" yaml:"secret_providers,omitempty" toml:"secret_providers,omitempty"`
	GlobalPreDeploy  []string                 `json:"globalPreDeploy,omitempty" yaml:"global_pre_deploy,omitempty" toml:"global_pre_deploy,omitempty"`
	GlobalPostDeploy []string                 `json:"globalPostDeploy,omitempty" yaml:"global_post_deploy,omitempty" toml:"global_post_deploy,omitempty"`
}

type TargetConfig struct {
	// Name is the application name for this deployment.
	// In a multi-target file, if this is omitted, the map key from 'targets' is used.
	// In a single-deployment file, this is required at the top level.
	Name string `json:"name,omitempty" yaml:"name,omitempty" toml:"name,omitempty"`

	// Preset applies default configurations for specific use cases, like databases.
	Preset Preset `json:"preset,omitempty" yaml:"preset,omitempty" toml:"preset,omitempty"`

	// Image can be defined inline OR reference a named image (ImageKey) from the Images map
	Image              *Image             `json:"image,omitempty" yaml:"image,omitempty" toml:"image,omitempty"`
	ImageKey           string             `json:"imageKey,omitempty" yaml:"image_key,omitempty" toml:"image_key,omitempty"`
	Server             string             `json:"server,omitempty" yaml:"server,omitempty" toml:"server,omitempty"`
	APIToken           *ValueSource       `json:"apiToken,omitempty" yaml:"api_token,omitempty" toml:"api_token,omitempty"`
	DeploymentStrategy DeploymentStrategy `json:"deploymentStrategy,omitempty" yaml:"deployment_strategy,omitempty" toml:"deployment_strategy,omitempty"`
	NamingStrategy     NamingStrategy     `json:"namingStrategy,omitempty" yaml:"naming_strategy,omitempty" toml:"naming_strategy,omitempty"`
	Protected          *bool              `json:"protected,omitempty" yaml:"protected,omitempty" toml:"protected,omitempty"`
	Domains            []Domain           `json:"domains,omitempty" yaml:"domains,omitempty" toml:"domains,omitempty"`
	Env                []EnvVar           `json:"env,omitempty" yaml:"env,omitempty" toml:"env,omitempty"`
	HealthCheckPath    string             `json:"healthCheckPath,omitempty" yaml:"health_check_path,omitempty" toml:"health_check_path,omitempty"`
	MinReadySeconds    *int               `json:"minReadySeconds,omitempty" yaml:"min_ready_seconds,omitempty" toml:"min_ready_seconds,omitempty"`
	Port               Port               `json:"port,omitempty" yaml:"port,omitempty" toml:"port,omitempty"`
	Replicas           *int               `json:"replicas,omitempty" yaml:"replicas,omitempty" toml:"replicas,omitempty"`
	Volumes            []string           `json:"volumes,omitempty" yaml:"volumes,omitempty" toml:"volumes,omitempty"`
	Network            string             `json:"network,omitempty" yaml:"network,omitempty" toml:"network,omitempty"`
	PreDeploy          []string           `json:"preDeploy,omitempty" yaml:"pre_deploy,omitempty" toml:"pre_deploy,omitempty"`
	PostDeploy         []string           `json:"postDeploy,omitempty" yaml:"post_deploy,omitempty" toml:"post_deploy,omitempty"`

	// Command overrides the image's CMD. Application images normally carry the
	// right one, but the side containers a `database` or `service` preset is
	// for are usually stock images configured entirely on the command line —
	// `postgres -c shared_buffers=128MB`, `nats --jetstream --store_dir /data`.
	// Without this they can only be tuned by baking a derived image.
	Command []string `json:"command,omitempty" yaml:"command,omitempty" toml:"command,omitempty"`

	// Resources caps what this container may take from the host.
	Resources *Resources `json:"resources,omitempty" yaml:"resources,omitempty" toml:"resources,omitempty"`

	// Hostname sets the container's own hostname. Docker otherwise uses the
	// container ID, which is fine until something inside derives an identity
	// from it — an Erlang or Elixir release names its node
	// `<name>@<hostname>`, so with a rolling deployment the node is called
	// something new every time. Left empty, Docker's default stands.
	Hostname string `json:"hostname,omitempty" yaml:"hostname,omitempty" toml:"hostname,omitempty"`

	// Publish maps container ports onto the host, in Docker's own
	// "[host_ip:]host_port:container_port[/proto]" form. Public traffic
	// belongs behind the proxy and should use `domains` instead; this is for
	// the doors that must not go through it — an admin dashboard or a
	// database socket bound to 127.0.0.1 and reached over an SSH tunnel.
	Publish []string `json:"publish,omitempty" yaml:"publish,omitempty" toml:"publish,omitempty"`

	// HealthCheck is Docker's own HEALTHCHECK — distinct from HealthCheckPath,
	// which is the HTTP probe haloy runs itself to decide whether a replica
	// takes traffic. Containers with no HTTP surface (a database, a broker)
	// have nothing to give the latter, and this is how they report health.
	HealthCheck *HealthCheck `json:"healthcheck,omitempty" yaml:"healthcheck,omitempty" toml:"healthcheck,omitempty"`

	// Non config fields. Not read from the config file and populated on load.
	TargetName string `json:"-" yaml:"-" toml:"-"`
	Format     string `json:"-" yaml:"-" toml:"-"`
}

type Preset string

const (
	PresetDefault  Preset = "default"
	PresetDatabase Preset = "database"
	PresetService  Preset = "service"
)

type DeploymentStrategy string

const (
	DeploymentStrategyRolling DeploymentStrategy = "rolling" // Default: blue-green deployments
	DeploymentStrategyReplace DeploymentStrategy = "replace" // Stop old, start new
)

type NamingStrategy string

const (
	NamingStrategyDynamic NamingStrategy = "dynamic" // Default: app-deploymentID
	NamingStrategyStatic  NamingStrategy = "static"  // app (requires replace strategy)
)

type Domain struct {
	Canonical string   `yaml:"domain" json:"domain" toml:"domain"`
	Aliases   []string `yaml:"aliases,omitempty" json:"aliases,omitempty" toml:"aliases,omitempty"`
}

func (d *Domain) Validate() error {
	if err := helpers.IsValidDomain(d.Canonical); err != nil {
		return err
	}

	for _, alias := range d.Aliases {
		if err := helpers.IsValidDomain(alias); err != nil {
			return fmt.Errorf("alias '%s': %w", alias, err)
		}
	}
	return nil
}

// Resources caps a container's share of the host.
//
// Both fields are optional; an empty one means "no limit", which is Docker's
// own default. They matter most on a small single box running several
// containers side by side, where one of them growing without a ceiling takes
// the others down with it.
type Resources struct {
	// Memory is a byte size with an optional suffix: "512m", "2g", "1048576".
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty" toml:"memory,omitempty"`
	// CPUs is a fractional core count, as in Docker's own --cpus: "0.5", "2".
	CPUs string `json:"cpus,omitempty" yaml:"cpus,omitempty" toml:"cpus,omitempty"`
}

// MemoryBytes returns the memory limit in bytes, or 0 when unset.
func (r *Resources) MemoryBytes() (int64, error) {
	if r == nil || r.Memory == "" {
		return 0, nil
	}
	bytes, err := units.RAMInBytes(r.Memory)
	if err != nil {
		return 0, fmt.Errorf("invalid memory '%s': %w", r.Memory, err)
	}
	if bytes <= 0 {
		return 0, fmt.Errorf("invalid memory '%s': must be positive", r.Memory)
	}
	return bytes, nil
}

// NanoCPUs returns the CPU limit in nano-CPUs, or 0 when unset.
func (r *Resources) NanoCPUs() (int64, error) {
	if r == nil || r.CPUs == "" {
		return 0, nil
	}
	cpus, err := strconv.ParseFloat(r.CPUs, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpus '%s': %w", r.CPUs, err)
	}
	if cpus <= 0 {
		return 0, fmt.Errorf("invalid cpus '%s': must be positive", r.CPUs)
	}
	return int64(cpus * 1e9), nil
}

func (r *Resources) Validate() error {
	if _, err := r.MemoryBytes(); err != nil {
		return err
	}
	_, err := r.NanoCPUs()
	return err
}

// HealthCheck mirrors Docker's HEALTHCHECK.
type HealthCheck struct {
	// Test is the check itself, in Docker's own form: the first element is
	// "CMD" or "CMD-SHELL" and the rest is the command, e.g.
	// ["CMD-SHELL", "pg_isready -U postgres"].
	Test []string `json:"test" yaml:"test" toml:"test"`
	// Interval, Timeout and StartPeriod are Go durations ("10s", "1m").
	// An empty one leaves Docker's default in place.
	Interval    string `json:"interval,omitempty" yaml:"interval,omitempty" toml:"interval,omitempty"`
	Timeout     string `json:"timeout,omitempty" yaml:"timeout,omitempty" toml:"timeout,omitempty"`
	StartPeriod string `json:"startPeriod,omitempty" yaml:"start_period,omitempty" toml:"start_period,omitempty"`
	// Retries is how many consecutive failures make the container unhealthy.
	Retries int `json:"retries,omitempty" yaml:"retries,omitempty" toml:"retries,omitempty"`
}

// Durations parses Interval, Timeout and StartPeriod. An empty field comes
// back as 0, which Docker reads as "use the default".
func (h *HealthCheck) Durations() (interval, timeout, startPeriod time.Duration, err error) {
	if h == nil {
		return 0, 0, 0, nil
	}
	for _, field := range []struct {
		name  string
		value string
		into  *time.Duration
	}{
		{"interval", h.Interval, &interval},
		{"timeout", h.Timeout, &timeout},
		{"start_period", h.StartPeriod, &startPeriod},
	} {
		if field.value == "" {
			continue
		}
		d, parseErr := time.ParseDuration(field.value)
		if parseErr != nil {
			return 0, 0, 0, fmt.Errorf("invalid healthcheck %s '%s': %w", field.name, field.value, parseErr)
		}
		if d <= 0 {
			return 0, 0, 0, fmt.Errorf("invalid healthcheck %s '%s': must be positive", field.name, field.value)
		}
		*field.into = d
	}
	return interval, timeout, startPeriod, nil
}

func (h *HealthCheck) Validate() error {
	if h == nil {
		return nil
	}
	if len(h.Test) == 0 {
		return errors.New("healthcheck test cannot be empty")
	}
	switch h.Test[0] {
	case "CMD", "CMD-SHELL", "NONE":
	default:
		return fmt.Errorf("healthcheck test must start with 'CMD', 'CMD-SHELL' or 'NONE', got '%s'", h.Test[0])
	}
	if h.Retries < 0 {
		return errors.New("healthcheck retries cannot be negative")
	}
	_, _, _, err := h.Durations()
	return err
}

type EnvVar struct {
	Name        string `json:"name" yaml:"name" toml:"name"`
	ValueSource `mapstructure:",squash" json:",inline" yaml:",inline" toml:",inline"`
	// BuildArg indicates this environment variable should also be included as a build argument.
	BuildArg bool `json:"buildArg,omitempty" yaml:"build_arg,omitempty" toml:"build_arg,omitempty"`
}

func (ev *EnvVar) Validate(format string) error {
	if ev.Name == "" {
		return errors.New("environment variable 'name' cannot be empty")
	}

	if err := ev.ValueSource.Validate(); err != nil {
		return fmt.Errorf("environment variable '%s': %w", ev.Name, err)
	}

	return nil
}

// Using custom Port type so we can use both string and int for port in the config.
type Port string

func (p Port) String() string {
	return string(p)
}

func PortDecodeHook() mapstructure.DecodeHookFuncType {
	return func(
		f reflect.Type,
		t reflect.Type,
		data any,
	) (any, error) {
		// Only process if target type is Port
		if t != reflect.TypeFor[Port]() {
			return data, nil
		}

		switch v := data.(type) {
		case string:
			return Port(v), nil
		case int:
			return Port(strconv.Itoa(v)), nil
		case int64:
			return Port(strconv.FormatInt(v, 10)), nil
		case float64:
			// Handle case where YAML/JSON might parse integers as floats
			if v == float64(int(v)) {
				return Port(strconv.Itoa(int(v))), nil
			}
			return nil, fmt.Errorf("port must be an integer, got float: %v", v)
		default:
			return nil, fmt.Errorf("port must be a string or integer, got %T: %v", data, data)
		}
	}
}
