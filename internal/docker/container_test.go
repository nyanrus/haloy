package docker

import (
	"strings"
	"testing"
	"time"

	"github.com/haloydev/haloy/internal/config"
)

func TestHealthConfigFor(t *testing.T) {
	t.Run("nil leaves the image's own HEALTHCHECK alone", func(t *testing.T) {
		got, err := healthConfigFor(nil)
		if err != nil {
			t.Fatalf("healthConfigFor() error = %v", err)
		}
		if got != nil {
			t.Errorf("healthConfigFor(nil) = %+v, want nil", got)
		}
	})

	t.Run("carries the whole check across", func(t *testing.T) {
		got, err := healthConfigFor(&config.HealthCheck{
			Test:        []string{"CMD-SHELL", "pg_isready -U postgres"},
			Interval:    "5s",
			Timeout:     "3s",
			StartPeriod: "30s",
			Retries:     5,
		})
		if err != nil {
			t.Fatalf("healthConfigFor() error = %v", err)
		}
		if len(got.Test) != 2 || got.Test[0] != "CMD-SHELL" {
			t.Errorf("Test = %v, want [CMD-SHELL pg_isready -U postgres]", got.Test)
		}
		if got.Interval != 5*time.Second {
			t.Errorf("Interval = %v, want 5s", got.Interval)
		}
		if got.Timeout != 3*time.Second {
			t.Errorf("Timeout = %v, want 3s", got.Timeout)
		}
		if got.StartPeriod != 30*time.Second {
			t.Errorf("StartPeriod = %v, want 30s", got.StartPeriod)
		}
		if got.Retries != 5 {
			t.Errorf("Retries = %d, want 5", got.Retries)
		}
	})

	t.Run("an unparseable duration is an error, not a zero", func(t *testing.T) {
		_, err := healthConfigFor(&config.HealthCheck{
			Test:     []string{"CMD", "true"},
			Interval: "soon",
		})
		if err == nil || !strings.Contains(err.Error(), "invalid healthcheck interval") {
			t.Fatalf("healthConfigFor() error = %v, want it to mention the interval", err)
		}
	})
}
