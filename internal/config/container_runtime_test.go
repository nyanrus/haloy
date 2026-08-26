package config

import (
	"strings"
	"testing"
	"time"
)

func TestResourcesMemoryBytes(t *testing.T) {
	tests := []struct {
		name      string
		resources *Resources
		want      int64
		wantErr   string
	}{
		{name: "nil resources", resources: nil, want: 0},
		{name: "unset", resources: &Resources{}, want: 0},
		{name: "megabytes", resources: &Resources{Memory: "768m"}, want: 768 * 1024 * 1024},
		{name: "gigabytes", resources: &Resources{Memory: "3g"}, want: 3 * 1024 * 1024 * 1024},
		{name: "plain bytes", resources: &Resources{Memory: "1048576"}, want: 1048576},
		{name: "garbage", resources: &Resources{Memory: "lots"}, wantErr: "invalid memory"},
		{name: "zero", resources: &Resources{Memory: "0"}, wantErr: "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.resources.MemoryBytes()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("MemoryBytes() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MemoryBytes() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("MemoryBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResourcesNanoCPUs(t *testing.T) {
	tests := []struct {
		name      string
		resources *Resources
		want      int64
		wantErr   string
	}{
		{name: "nil resources", resources: nil, want: 0},
		{name: "unset", resources: &Resources{}, want: 0},
		{name: "fraction", resources: &Resources{CPUs: "0.5"}, want: 500_000_000},
		{name: "whole core", resources: &Resources{CPUs: "2"}, want: 2_000_000_000},
		{name: "garbage", resources: &Resources{CPUs: "half"}, wantErr: "invalid cpus"},
		{name: "negative", resources: &Resources{CPUs: "-1"}, wantErr: "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.resources.NanoCPUs()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NanoCPUs() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NanoCPUs() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("NanoCPUs() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHealthCheckDurations(t *testing.T) {
	hc := &HealthCheck{
		Test:        []string{"CMD-SHELL", "pg_isready -U postgres"},
		Interval:    "5s",
		Timeout:     "5s",
		StartPeriod: "1m",
	}

	interval, timeout, startPeriod, err := hc.Durations()
	if err != nil {
		t.Fatalf("Durations() error = %v", err)
	}
	if interval != 5*time.Second {
		t.Errorf("interval = %v, want 5s", interval)
	}
	if timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", timeout)
	}
	if startPeriod != time.Minute {
		t.Errorf("startPeriod = %v, want 1m", startPeriod)
	}
}

func TestHealthCheckDurationsLeavesUnsetFieldsAtZero(t *testing.T) {
	hc := &HealthCheck{Test: []string{"CMD", "true"}, Interval: "10s"}

	interval, timeout, startPeriod, err := hc.Durations()
	if err != nil {
		t.Fatalf("Durations() error = %v", err)
	}
	if interval != 10*time.Second {
		t.Errorf("interval = %v, want 10s", interval)
	}
	// Zero is what Docker reads as "use the default", so an unset field must
	// not turn into anything else.
	if timeout != 0 || startPeriod != 0 {
		t.Errorf("timeout = %v, startPeriod = %v, want both 0", timeout, startPeriod)
	}
}

func TestHealthCheckValidate(t *testing.T) {
	tests := []struct {
		name        string
		healthCheck *HealthCheck
		wantErr     string
	}{
		{name: "nil is fine", healthCheck: nil},
		{name: "cmd shell", healthCheck: &HealthCheck{Test: []string{"CMD-SHELL", "pg_isready"}}},
		{name: "cmd", healthCheck: &HealthCheck{Test: []string{"CMD", "wget", "-qO-", "http://localhost:8222/healthz"}}},
		{name: "none disables the image's own", healthCheck: &HealthCheck{Test: []string{"NONE"}}},
		{name: "empty test", healthCheck: &HealthCheck{}, wantErr: "cannot be empty"},
		{
			name:        "bare command without CMD prefix",
			healthCheck: &HealthCheck{Test: []string{"pg_isready"}},
			wantErr:     "must start with",
		},
		{
			name:        "negative retries",
			healthCheck: &HealthCheck{Test: []string{"CMD", "true"}, Retries: -1},
			wantErr:     "cannot be negative",
		},
		{
			name:        "unparseable interval",
			healthCheck: &HealthCheck{Test: []string{"CMD", "true"}, Interval: "soon"},
			wantErr:     "invalid healthcheck interval",
		},
		{
			name:        "zero interval",
			healthCheck: &HealthCheck{Test: []string{"CMD", "true"}, Interval: "0s"},
			wantErr:     "must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.healthCheck.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestHostnameValidation(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		wantErr  bool
	}{
		{name: "bare name", hostname: "web-gw"},
		{name: "dotted", hostname: "deployex.internal"},
		{name: "no TLD required", hostname: "anubis"},
		{name: "leading hyphen", hostname: "-bad", wantErr: true},
		{name: "underscore", hostname: "not_a_hostname", wantErr: true},
		{name: "trailing dot", hostname: "trailing.", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := &TargetConfig{
				Name:     "app",
				Server:   "test.haloy.dev",
				Image:    &Image{Repository: "nginx"},
				Hostname: tt.hostname,
			}
			err := tc.Validate("yaml")
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() error = nil, want an error for hostname %q", tt.hostname)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v, want nil for hostname %q", err, tt.hostname)
			}
		})
	}
}

func TestPublishValidation(t *testing.T) {
	tests := []struct {
		name     string
		publish  []string
		strategy DeploymentStrategy
		wantErr  string
	}{
		{
			name:     "loopback admin port with replace",
			publish:  []string{"127.0.0.1:5001:5001"},
			strategy: DeploymentStrategyReplace,
		},
		{
			name:     "several ports",
			publish:  []string{"127.0.0.1:5432:5432", "127.0.0.1:4222:4222"},
			strategy: DeploymentStrategyReplace,
		},
		{
			name:     "rolling cannot hold a host port twice",
			publish:  []string{"127.0.0.1:5001:5001"},
			strategy: DeploymentStrategyRolling,
			wantErr:  "requires",
		},
		{
			name:     "nonsense spec",
			publish:  []string{"this is not a port"},
			strategy: DeploymentStrategyReplace,
			wantErr:  "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := &TargetConfig{
				Name:               "app",
				Server:             "test.haloy.dev",
				Image:              &Image{Repository: "nginx"},
				Publish:            tt.publish,
				DeploymentStrategy: tt.strategy,
			}
			err := tc.Validate("yaml")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
