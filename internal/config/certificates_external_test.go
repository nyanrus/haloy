package config

import "testing"

func TestCertificatesConfigIsExternal(t *testing.T) {
	cfg := CertificatesConfig{External: []string{"natadeco.example", "Sukhi.Example.Test"}}

	tests := []struct {
		name   string
		domain string
		want   bool
	}{
		{name: "listed", domain: "natadeco.example", want: true},
		{name: "listed, different case", domain: "sukhi.example.test", want: true},
		{name: "listed, as written", domain: "Sukhi.Example.Test", want: true},
		{name: "not listed", domain: "other.example", want: false},
		{name: "an alias is not the canonical", domain: "www.natadeco.example", want: false},
		{name: "empty", domain: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.IsExternal(tt.domain); got != tt.want {
				t.Errorf("IsExternal(%q) = %v, want %v", tt.domain, got, tt.want)
			}
		})
	}
}

func TestCertificatesConfigEmptyMeansNothingIsExternal(t *testing.T) {
	var cfg CertificatesConfig
	if cfg.IsExternal("anything.example") {
		t.Error("IsExternal() on an empty config = true, want false")
	}
}
