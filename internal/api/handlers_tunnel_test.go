package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func newTunnelTestServer(t *testing.T, policy TunnelPolicy) *APIServer {
	t.Helper()
	s := NewServer("secret", nil, nil, slog.LevelError, policy)
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return s
}

func TestTunnelHandlerRequiresTCPUpgrade(t *testing.T) {
	s := newTunnelTestServer(t, DefaultTunnelPolicy())
	req := httptest.NewRequest(http.MethodPost, "/v1/tunnel/postgres", nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()

	s.router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestTunnelHandlerValidatesPortServerSide(t *testing.T) {
	s := newTunnelTestServer(t, DefaultTunnelPolicy())
	req := httptest.NewRequest(http.MethodPost, "/v1/tunnel/postgres?port=70000", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	recorder := httptest.NewRecorder()

	s.router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestTunnelHandlerProxiesBytes(t *testing.T) {
	s := newTunnelTestServer(t, DefaultTunnelPolicy())
	s.tunnelResolver = func(context.Context, string, string, string) (tunnelTarget, error) {
		return tunnelTarget{address: "fake:5432", containerID: strings.Repeat("a", 64), port: "5432"}, nil
	}
	s.tunnelDialer = func(context.Context, string, string) (net.Conn, error) {
		handlerSide, backendSide := net.Pipe()
		go func() {
			defer backendSide.Close()
			_, _ = io.Copy(backendSide, backendSide)
		}()
		return handlerSide, nil
	}

	httpServer := httptest.NewServer(s.router)
	defer httpServer.Close()

	conn, reader := openRawTunnel(t, httpServer.URL)
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write tunnel payload: %v", err)
	}
	payload := make([]byte, 4)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read tunnel payload: %v", err)
	}
	if string(payload) != "ping" {
		t.Fatalf("payload = %q, want ping", payload)
	}
}

func TestTunnelCapacityLimitsOpenConnections(t *testing.T) {
	capacity := newTunnelCapacity(TunnelPolicy{MaxOpen: 2, MaxOpenPerClient: 1})
	if !capacity.acquire("198.51.100.1") {
		t.Fatal("first client acquisition rejected")
	}
	if capacity.acquire("198.51.100.1") {
		t.Fatal("per-client limit was not enforced")
	}
	if !capacity.acquire("198.51.100.2") {
		t.Fatal("second client acquisition rejected")
	}
	if capacity.acquire("198.51.100.3") {
		t.Fatal("global limit was not enforced")
	}
	capacity.release("198.51.100.1")
	if !capacity.acquire("198.51.100.3") {
		t.Fatal("capacity was not released")
	}
}

func TestTunnelTrackerForceClosesActiveHandler(t *testing.T) {
	s := newTunnelTestServer(t, DefaultTunnelPolicy())
	s.tunnelResolver = func(context.Context, string, string, string) (tunnelTarget, error) {
		return tunnelTarget{address: "fake:5432", containerID: strings.Repeat("b", 64), port: "5432"}, nil
	}
	backendClosed := make(chan struct{})
	s.tunnelDialer = func(context.Context, string, string) (net.Conn, error) {
		handlerSide, backendSide := net.Pipe()
		go func() {
			defer close(backendClosed)
			defer backendSide.Close()
			_, _ = io.Copy(io.Discard, backendSide)
		}()
		return handlerSide, nil
	}

	httpServer := httptest.NewServer(s.router)
	defer httpServer.Close()
	conn, _ := openRawTunnel(t, httpServer.URL)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := s.tunnelTracker.shutdown(ctx)
	if err == nil || !strings.Contains(err.Error(), "force-closed 1 tunnel") {
		t.Fatalf("shutdown error = %v, want force-close result", err)
	}

	select {
	case <-backendClosed:
	case <-time.After(time.Second):
		t.Fatal("backend connection remained open after tracker shutdown")
	}
}

func TestTunnelContainerIPUsesDefaultOrSingleCustomNetwork(t *testing.T) {
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"custom": {IPAddress: "172.20.0.5"},
			},
		},
	}
	ip, err := tunnelContainerIP(info, false)
	if err != nil {
		t.Fatalf("tunnelContainerIP() error = %v", err)
	}
	if ip != "172.20.0.5" {
		t.Fatalf("IP = %q, want custom network address", ip)
	}

	hostIP, err := tunnelContainerIP(container.InspectResponse{}, true)
	if err != nil || hostIP != "127.0.0.1" {
		t.Fatalf("host network IP = %q, %v", hostIP, err)
	}
}

func TestSelectTunnelContainerRejectsAmbiguousPrefix(t *testing.T) {
	containers := []container.Summary{
		{ID: "abc111"},
		{ID: "abc222"},
	}
	_, err := selectTunnelContainer(containers, "abc")
	var requestErr *tunnelRequestError
	if !errors.As(err, &requestErr) || requestErr.status != http.StatusConflict {
		t.Fatalf("selectTunnelContainer() error = %v, want 409 conflict", err)
	}
}

func TestSelectTunnelPortRejectsHostNetworkOverrideByDefault(t *testing.T) {
	_, err := selectTunnelPort("5432", "6432", true, false)
	var requestErr *tunnelRequestError
	if !errors.As(err, &requestErr) || requestErr.status != http.StatusForbidden {
		t.Fatalf("selectTunnelPort() error = %v, want 403 forbidden", err)
	}

	port, err := selectTunnelPort("5432", "6432", true, true)
	if err != nil || port != "6432" {
		t.Fatalf("allowed host override = %q, %v", port, err)
	}
}

func openRawTunnel(t *testing.T, serverURL string) (net.Conn, *bufio.Reader) {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	request := "POST /v1/tunnel/postgres HTTP/1.1\r\n" +
		"Host: " + parsed.Host + "\r\n" +
		"Authorization: Bearer secret\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: tcp\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/tunnel/postgres", nil)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	return conn, reader
}
