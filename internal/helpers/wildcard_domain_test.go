package helpers

import "testing"

func TestWildcardDomain(t *testing.T) {
	tests := []struct{ domain, want string }{
		{"one.example.test", "*.example.test"},
		{"a.b.c.d", "*.b.c.d"},
		{"example.test", ""}, // an apex has no parent to stand in for
		{"localhost", ""},
	}
	for _, tt := range tests {
		if got := WildcardDomain(tt.domain); got != tt.want {
			t.Errorf("WildcardDomain(%q) = %q, want %q", tt.domain, got, tt.want)
		}
	}
}
