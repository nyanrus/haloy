package haloy

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/haloydev/haloy/internal/config"
)

func TestResolveTunnelPorts(t *testing.T) {
	target := config.TargetConfig{
		Name: "postgres",
		Port: config.Port("5432"),
	}

	tests := []struct {
		name           string
		args           []string
		localPortFlag  string
		remotePortFlag string
		wantLocal      string
		wantRemote     string
	}{
		{
			name:      "defaults local port from target config",
			wantLocal: "5432",
		},
		{
			name:          "uses port flag as local port",
			localPortFlag: "25432",
			wantLocal:     "25432",
		},
		{
			name:      "uses positional argument as local port",
			args:      []string{"15432"},
			wantLocal: "15432",
		},
		{
			name:           "uses remote port flag as remote override",
			remotePortFlag: "15432",
			wantLocal:      "5432",
			wantRemote:     "15432",
		},
		{
			name:           "uses local and remote overrides together",
			localPortFlag:  "25432",
			remotePortFlag: "5433",
			wantLocal:      "25432",
			wantRemote:     "5433",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLocal, gotRemote, err := resolveTunnelPorts(target, tt.args, tt.localPortFlag, tt.remotePortFlag)
			if err != nil {
				t.Fatalf("resolveTunnelPorts error = %v", err)
			}
			if gotLocal != tt.wantLocal {
				t.Fatalf("local port = %q, want %q", gotLocal, tt.wantLocal)
			}
			if gotRemote != tt.wantRemote {
				t.Fatalf("remote port = %q, want %q", gotRemote, tt.wantRemote)
			}
		})
	}
}

func TestTunnelServerAddress(t *testing.T) {
	tests := []struct {
		host   string
		useTLS bool
		want   string
	}{
		{host: "example.com", useTLS: true, want: "example.com:443"},
		{host: "example.com:8443", useTLS: true, want: "example.com:8443"},
		{host: "127.0.0.1", want: "127.0.0.1:80"},
		{host: "[::1]", want: "[::1]:80"},
		{host: "[2001:db8::1]:8443", useTLS: true, want: "[2001:db8::1]:8443"},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got, err := tunnelServerAddress(tt.host, tt.useTLS)
			if err != nil {
				t.Fatalf("tunnelServerAddress() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("tunnelServerAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDialTunnelSuccessfulUpgradeAndEscapedQuery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()

		req, readErr := http.ReadRequest(bufio.NewReader(conn))
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if got := req.URL.Query().Get("container"); got != "abc&port=1" {
			serverErr <- &testError{message: "container query was not preserved", got: got}
			return
		}
		if req.Header.Get("Authorization") != "Bearer secret" {
			serverErr <- &testError{message: "authorization header missing", got: req.Header.Get("Authorization")}
			return
		}
		if _, writeErr := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\nhello"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		serverErr <- nil
	}()

	conn, err := dialTunnel(context.Background(), listener.Addr().String(), false, "secret", "postgres", "5432", "abc&port=1")
	if err != nil {
		t.Fatalf("dialTunnel() error = %v", err)
	}
	defer conn.Close()

	payload := make([]byte, len("hello"))
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatalf("read tunneled payload: %v", err)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q, want hello", payload)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestDialTunnelReadsBoundedKeepAliveErrorWithoutWaitingForEOF(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	releaseServer := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		if _, readErr := http.ReadRequest(bufio.NewReader(conn)); readErr != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 3\r\nConnection: keep-alive\r\n\r\nbad")
		<-releaseServer
	}()
	defer close(releaseServer)

	result := make(chan error, 1)
	go func() {
		_, dialErr := dialTunnel(context.Background(), listener.Addr().String(), false, "secret", "postgres", "", "")
		result <- dialErr
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "status 400: bad") {
			t.Fatalf("dialTunnel() error = %v, want bounded status error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dialTunnel waited for connection EOF instead of Content-Length")
	}
}

func TestDialTunnelRejectsWrongUpgradeProtocol(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		if _, readErr := http.ReadRequest(bufio.NewReader(conn)); readErr != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	}()

	_, err = dialTunnel(context.Background(), listener.Addr().String(), false, "secret", "postgres", "", "")
	if err == nil || !strings.Contains(err.Error(), "invalid tunnel upgrade response") {
		t.Fatalf("dialTunnel() error = %v, want invalid upgrade error", err)
	}
}

func TestDialTunnelRejectsOversizedResponseHeaders(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		if _, readErr := http.ReadRequest(bufio.NewReader(conn)); readErr != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nX-Large: "+strings.Repeat("a", maxTunnelHeaderBytes)+"\r\n\r\n")
	}()

	_, err = dialTunnel(context.Background(), listener.Addr().String(), false, "secret", "postgres", "", "")
	if err == nil || !strings.Contains(err.Error(), "headers exceed") {
		t.Fatalf("dialTunnel() error = %v, want oversized header error", err)
	}
}

type testError struct {
	message string
	got     string
}

func (e *testError) Error() string {
	return e.message + ": " + e.got
}

func TestResolveTunnelPortsErrors(t *testing.T) {
	target := config.TargetConfig{
		Name: "postgres",
		Port: config.Port("5432"),
	}

	tests := []struct {
		name           string
		target         config.TargetConfig
		args           []string
		localPortFlag  string
		remotePortFlag string
		wantErr        string
	}{
		{
			name:          "rejects duplicate local port",
			target:        target,
			args:          []string{"15432"},
			localPortFlag: "25432",
			wantErr:       "local port specified twice",
		},
		{
			name:    "requires local port when target has no configured port",
			target:  config.TargetConfig{Name: "worker"},
			wantErr: "no port configured for target",
		},
		{
			name:          "rejects invalid local port",
			target:        target,
			localPortFlag: "70000",
			wantErr:       "invalid local port",
		},
		{
			name:           "rejects invalid remote port",
			target:         target,
			remotePortFlag: "not-a-port",
			wantErr:        "invalid remote port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := resolveTunnelPorts(tt.target, tt.args, tt.localPortFlag, tt.remotePortFlag)
			if err == nil {
				t.Fatal("resolveTunnelPorts error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("resolveTunnelPorts error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
