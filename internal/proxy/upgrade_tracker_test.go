package proxy

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestShutdownForceClosesAPIUpgradeTunnel(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendListener.Close()

	backendClosed := make(chan struct{})
	go func() {
		conn, acceptErr := backendListener.Accept()
		if acceptErr != nil {
			close(backendClosed)
			return
		}
		defer close(backendClosed)
		defer conn.Close()
		if _, readErr := http.ReadRequest(bufio.NewReader(conn)); readErr != nil {
			return
		}
		if _, writeErr := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n"); writeErr != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
	}()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(logger, nil)
	host, port, err := net.SplitHostPort(backendListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	builder := NewRouteBuilder()
	builder.SetAPIBackend(host, port)
	config, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	p.UpdateConfig(config)

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.proxyToAPIBackend(w, r, time.Now())
	}))
	defer front.Close()

	parsed, err := url.Parse(front.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "POST /v1/tunnel/postgres HTTP/1.1\r\n" +
		"Host: api.example.com\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	req, err := http.NewRequest(http.MethodPost, front.URL+"/v1/tunnel/postgres", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err = p.Shutdown(ctx)
	if err == nil || !strings.Contains(err.Error(), "websocket/tunnel") {
		t.Fatalf("Shutdown() error = %v, want tunnel force-close error", err)
	}

	select {
	case <-backendClosed:
	case <-time.After(time.Second):
		t.Fatal("backend API upgrade connection remained open")
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("client tunnel remained readable after proxy shutdown")
	}
}
