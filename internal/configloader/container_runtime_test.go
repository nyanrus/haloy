package configloader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haloydev/haloy/internal/config"
)

// The side containers a `database` preset is for are configured on the command
// line and need a ceiling on a shared box, so read a config that looks like one.
func TestLoadRawDeployConfig_ContainerRuntimeOptions(t *testing.T) {
	yaml := `
name: db
server: test.haloy.dev
preset: database
image: "postgres:16-alpine"
command:
  - postgres
  - -c
  - shared_buffers=128MB
  - -c
  - max_connections=40
resources:
  memory: 300m
  cpus: "0.3"
healthcheck:
  test: ["CMD-SHELL", "pg_isready -U postgres"]
  interval: 5s
  timeout: 5s
  retries: 5
  start_period: 30s
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "haloy.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	dc, _, err := LoadRawDeployConfig(configPath)
	if err != nil {
		t.Fatalf("LoadRawDeployConfig() unexpected error = %v", err)
	}

	wantCommand := []string{"postgres", "-c", "shared_buffers=128MB", "-c", "max_connections=40"}
	if len(dc.Command) != len(wantCommand) {
		t.Fatalf("Command = %v, want %v", dc.Command, wantCommand)
	}
	for i, arg := range wantCommand {
		if dc.Command[i] != arg {
			t.Errorf("Command[%d] = %q, want %q", i, dc.Command[i], arg)
		}
	}

	if dc.Resources == nil {
		t.Fatal("Resources should not be nil")
	}
	memory, err := dc.Resources.MemoryBytes()
	if err != nil {
		t.Fatalf("MemoryBytes() error = %v", err)
	}
	if want := int64(300 * 1024 * 1024); memory != want {
		t.Errorf("MemoryBytes() = %d, want %d", memory, want)
	}
	cpus, err := dc.Resources.NanoCPUs()
	if err != nil {
		t.Fatalf("NanoCPUs() error = %v", err)
	}
	if want := int64(300_000_000); cpus != want {
		t.Errorf("NanoCPUs() = %d, want %d", cpus, want)
	}

	if dc.HealthCheck == nil {
		t.Fatal("HealthCheck should not be nil")
	}
	if got := dc.HealthCheck.Test; len(got) != 2 || got[0] != "CMD-SHELL" || got[1] != "pg_isready -U postgres" {
		t.Errorf("HealthCheck.Test = %v, want [CMD-SHELL pg_isready -U postgres]", got)
	}
	if dc.HealthCheck.Retries != 5 {
		t.Errorf("HealthCheck.Retries = %d, want 5", dc.HealthCheck.Retries)
	}
	if err := dc.HealthCheck.Validate(); err != nil {
		t.Errorf("HealthCheck.Validate() error = %v", err)
	}
}

// A target that says nothing about these should take the file's top-level ones.
func TestMergeToTarget_ContainerRuntimeOptionsInherit(t *testing.T) {
	base := config.DeployConfig{
		TargetConfig: config.TargetConfig{
			Name:      "myapp",
			Server:    "default.haloy.dev",
			Image:     &config.Image{Repository: "nginx", Tag: "1.20"},
			Command:   []string{"nginx", "-g", "daemon off;"},
			Resources: &config.Resources{Memory: "768m", CPUs: "0.5"},
			HealthCheck: &config.HealthCheck{
				Test:     []string{"CMD", "true"},
				Interval: "10s",
			},
		},
	}

	t.Run("inherits when the target is silent", func(t *testing.T) {
		tc, err := MergeToTarget(base, config.TargetConfig{}, "staging", "yaml")
		if err != nil {
			t.Fatalf("MergeToTarget() error = %v", err)
		}
		if len(tc.Command) != 3 || tc.Command[0] != "nginx" {
			t.Errorf("Command = %v, want the base's", tc.Command)
		}
		if tc.Resources == nil || tc.Resources.Memory != "768m" {
			t.Errorf("Resources = %+v, want the base's", tc.Resources)
		}
		if tc.HealthCheck == nil || tc.HealthCheck.Interval != "10s" {
			t.Errorf("HealthCheck = %+v, want the base's", tc.HealthCheck)
		}
	})

	t.Run("the target's own wins", func(t *testing.T) {
		tc, err := MergeToTarget(base, config.TargetConfig{
			Command:   []string{"sleep", "infinity"},
			Resources: &config.Resources{Memory: "96m"},
		}, "staging", "yaml")
		if err != nil {
			t.Fatalf("MergeToTarget() error = %v", err)
		}
		if len(tc.Command) != 2 || tc.Command[0] != "sleep" {
			t.Errorf("Command = %v, want the target's", tc.Command)
		}
		if tc.Resources == nil || tc.Resources.Memory != "96m" {
			t.Errorf("Resources = %+v, want the target's", tc.Resources)
		}
		// Untouched by the target, so still the base's.
		if tc.HealthCheck == nil || tc.HealthCheck.Interval != "10s" {
			t.Errorf("HealthCheck = %+v, want the base's", tc.HealthCheck)
		}
	})
}
