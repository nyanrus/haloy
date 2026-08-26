package haloyproxy

import (
	"strings"
	"testing"

	"github.com/haloydev/haloy/internal/constants"
)

func TestListenAddrs(t *testing.T) {
	tests := []struct {
		name      string
		httpEnv   string
		httpsEnv  string
		wantHTTP  string
		wantHTTPS string
		wantErr   string
	}{
		{
			name:      "unset falls back to 80 and 443",
			wantHTTP:  ":80",
			wantHTTPS: ":443",
		},
		{
			name:      "ports only",
			httpEnv:   ":8081",
			httpsEnv:  ":8444",
			wantHTTP:  ":8081",
			wantHTTPS: ":8444",
		},
		{
			name:      "bound to loopback",
			httpEnv:   "127.0.0.1:8081",
			httpsEnv:  "127.0.0.1:8444",
			wantHTTP:  "127.0.0.1:8081",
			wantHTTPS: "127.0.0.1:8444",
		},
		{
			name:      "one overridden, the other still default",
			httpsEnv:  ":8444",
			wantHTTP:  ":80",
			wantHTTPS: ":8444",
		},
		{
			name:    "a bare port number is not a host:port",
			httpEnv: "8081",
			wantErr: "invalid " + constants.EnvVarProxyHTTPAddr,
		},
		{
			name:     "the same address twice cannot bind twice",
			httpEnv:  ":8081",
			httpsEnv: ":8081",
			wantErr:  "separate listeners",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(constants.EnvVarProxyHTTPAddr, tt.httpEnv)
			t.Setenv(constants.EnvVarProxyHTTPSAddr, tt.httpsEnv)

			httpAddr, httpsAddr, err := listenAddrs()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("listenAddrs() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("listenAddrs() error = %v", err)
			}
			if httpAddr != tt.wantHTTP {
				t.Errorf("httpAddr = %q, want %q", httpAddr, tt.wantHTTP)
			}
			if httpsAddr != tt.wantHTTPS {
				t.Errorf("httpsAddr = %q, want %q", httpsAddr, tt.wantHTTPS)
			}
		})
	}
}
