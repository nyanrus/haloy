package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/haloydev/haloy/internal/config"
	"github.com/haloydev/haloy/internal/constants"
	"github.com/haloydev/haloy/internal/docker"
	"github.com/haloydev/haloy/internal/helpers"
)

const tunnelTargetDialTimeout = 10 * time.Second

var (
	tunnelAppNamePattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	tunnelContainerIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{1,64}$`)
)

type tunnelTarget struct {
	address     string
	containerID string
	port        string
}

type (
	tunnelTargetResolver func(context.Context, string, string, string) (tunnelTarget, error)
	tunnelDialer         func(context.Context, string, string) (net.Conn, error)
)

type tunnelRequestError struct {
	status  int
	message string
	err     error
}

func (e *tunnelRequestError) Error() string {
	if e.err == nil {
		return e.message
	}
	return fmt.Sprintf("%s: %v", e.message, e.err)
}

func tunnelError(status int, message string, err error) error {
	return &tunnelRequestError{status: status, message: message, err: err}
}

func defaultTunnelDialer(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: tunnelTargetDialTimeout}
	return dialer.DialContext(ctx, network, address)
}

func (s *APIServer) handleTunnel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appName := r.PathValue("appName")
		if !tunnelAppNamePattern.MatchString(appName) {
			http.Error(w, "Invalid app name", http.StatusBadRequest)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "tcp") ||
			!requestHeaderContainsToken(r.Header.Get("Connection"), "upgrade") {
			http.Error(w, "TCP upgrade required", http.StatusBadRequest)
			return
		}

		portOverride := r.URL.Query().Get("port")
		if portOverride != "" {
			if err := helpers.ValidatePort(portOverride); err != nil {
				http.Error(w, "Invalid port", http.StatusBadRequest)
				return
			}
		}

		containerID := strings.ToLower(r.URL.Query().Get("container"))
		if containerID != "" && !tunnelContainerIDPattern.MatchString(containerID) {
			http.Error(w, "Invalid container ID", http.StatusBadRequest)
			return
		}

		sourceIP := clientIP(r)
		if !s.tunnelCapacity.acquire(sourceIP) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Tunnel capacity reached", http.StatusTooManyRequests)
			return
		}
		defer s.tunnelCapacity.release(sourceIP)

		started := time.Now()
		target, err := s.tunnelResolver(r.Context(), appName, containerID, portOverride)
		if err != nil {
			var requestErr *tunnelRequestError
			if errors.As(err, &requestErr) {
				s.tunnelLogger().Warn("Tunnel target rejected",
					"app", appName,
					"source_ip", sourceIP,
					"status", requestErr.status,
					"error", requestErr.err)
				http.Error(w, requestErr.message, requestErr.status)
				return
			}
			s.tunnelLogger().Error("Tunnel target resolution failed",
				"app", appName,
				"source_ip", sourceIP,
				"error", err)
			http.Error(w, "Failed to resolve tunnel target", http.StatusInternalServerError)
			return
		}

		containerConn, err := s.tunnelDialer(r.Context(), "tcp", target.address)
		if err != nil {
			s.tunnelLogger().Warn("Tunnel backend connection failed",
				"app", appName,
				"container_id", helpers.SafeIDPrefix(target.containerID),
				"port", target.port,
				"source_ip", sourceIP,
				"error", err)
			http.Error(w, "Failed to connect to container", http.StatusBadGateway)
			return
		}
		defer containerConn.Close()

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
			return
		}

		clientConn, bufrw, err := hijacker.Hijack()
		if err != nil {
			s.tunnelLogger().Error("Tunnel hijack failed", "app", appName, "error", err)
			return
		}
		defer clientConn.Close()

		if !s.tunnelTracker.track(clientConn, containerConn) {
			return
		}
		defer s.tunnelTracker.untrack(clientConn, containerConn)

		if _, err = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			return
		}
		if err = bufrw.Flush(); err != nil {
			return
		}

		s.tunnelLogger().Info("Tunnel opened",
			"app", appName,
			"container_id", helpers.SafeIDPrefix(target.containerID),
			"port", target.port,
			"source_ip", sourceIP)

		type copyResult struct {
			direction string
			bytes     int64
			err       error
		}
		results := make(chan copyResult, 2)

		go func() {
			n, copyErr := io.Copy(containerConn, bufrw)
			if tcpConn, ok := containerConn.(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			}
			results <- copyResult{direction: "client_to_container", bytes: n, err: copyErr}
		}()

		go func() {
			n, copyErr := io.Copy(clientConn, containerConn)
			if tcpConn, ok := clientConn.(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			}
			results <- copyResult{direction: "container_to_client", bytes: n, err: copyErr}
		}()

		var (
			bytesIn  int64
			bytesOut int64
			closeErr error
		)
		for range 2 {
			result := <-results
			if result.direction == "client_to_container" {
				bytesIn = result.bytes
			} else {
				bytesOut = result.bytes
			}
			if result.err != nil {
				closeErr = errors.Join(closeErr, result.err)
			}
		}

		attrs := []any{
			"app", appName,
			"container_id", helpers.SafeIDPrefix(target.containerID),
			"port", target.port,
			"source_ip", sourceIP,
			"duration_ms", time.Since(started).Milliseconds(),
			"bytes_client_to_container", bytesIn,
			"bytes_container_to_client", bytesOut,
		}
		if closeErr != nil {
			attrs = append(attrs, "error", closeErr)
			s.tunnelLogger().Warn("Tunnel closed", attrs...)
		} else {
			s.tunnelLogger().Info("Tunnel closed", attrs...)
		}
	}
}

func (s *APIServer) tunnelLogger() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

func requestHeaderContainsToken(value, token string) bool {
	for part := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func (s *APIServer) resolveTunnelTarget(ctx context.Context, appName, containerID, portOverride string) (tunnelTarget, error) {
	cli, err := docker.NewClient(ctx)
	if err != nil {
		return tunnelTarget{}, tunnelError(http.StatusInternalServerError, "Docker is unavailable", err)
	}
	defer cli.Close()

	containers, err := docker.GetAppContainers(ctx, cli, false, appName)
	if err != nil {
		return tunnelTarget{}, tunnelError(http.StatusInternalServerError, "Failed to list app containers", err)
	}
	if len(containers) == 0 {
		return tunnelTarget{}, tunnelError(http.StatusNotFound, "No running containers found for the specified app", nil)
	}

	targetContainerID, err := selectTunnelContainer(containers, containerID)
	if err != nil {
		return tunnelTarget{}, err
	}

	containerInfo, err := cli.ContainerInspect(ctx, targetContainerID)
	if err != nil {
		return tunnelTarget{}, tunnelError(http.StatusInternalServerError, "Failed to inspect container", err)
	}
	if containerInfo.Config == nil {
		return tunnelTarget{}, tunnelError(http.StatusInternalServerError, "Container configuration is unavailable", nil)
	}

	labels, err := config.ParseContainerLabels(containerInfo.Config.Labels)
	if err != nil {
		return tunnelTarget{}, tunnelError(http.StatusInternalServerError, "Container labels are invalid", err)
	}

	hostNetwork := containerInfo.HostConfig != nil && containerInfo.HostConfig.NetworkMode == "host"
	port, err := selectTunnelPort(labels.Port.String(), portOverride, hostNetwork, s.tunnelPolicy.AllowHostNetworkPortOverride)
	if err != nil {
		return tunnelTarget{}, err
	}

	containerIP, err := tunnelContainerIP(containerInfo, hostNetwork)
	if err != nil {
		return tunnelTarget{}, tunnelError(http.StatusInternalServerError, "Container network is unavailable", err)
	}

	return tunnelTarget{
		address:     net.JoinHostPort(containerIP, port),
		containerID: targetContainerID,
		port:        port,
	}, nil
}

func selectTunnelContainer(containers []container.Summary, selector string) (string, error) {
	if len(containers) == 0 {
		return "", tunnelError(http.StatusNotFound, "No running containers found for the specified app", nil)
	}
	if selector == "" {
		return containers[0].ID, nil
	}

	var matches []string
	for _, candidate := range containers {
		if candidate.ID == selector {
			return candidate.ID, nil
		}
		if strings.HasPrefix(candidate.ID, selector) {
			matches = append(matches, candidate.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", tunnelError(http.StatusNotFound, "Specified container not found for this app", nil)
	case 1:
		return matches[0], nil
	default:
		return "", tunnelError(http.StatusConflict, "Container ID prefix is ambiguous", nil)
	}
}

func selectTunnelPort(labelPort, override string, hostNetwork, allowHostOverride bool) (string, error) {
	if hostNetwork && override != "" && !allowHostOverride {
		return "", tunnelError(http.StatusForbidden, "Port overrides are disabled for host-network containers", nil)
	}
	port := labelPort
	if override != "" {
		port = override
	}
	if err := helpers.ValidatePort(port); err != nil {
		return "", tunnelError(http.StatusBadRequest, "Container port is invalid", err)
	}
	return port, nil
}

func tunnelContainerIP(containerInfo container.InspectResponse, hostNetwork bool) (string, error) {
	if hostNetwork {
		return "127.0.0.1", nil
	}
	if containerInfo.NetworkSettings == nil {
		return "", fmt.Errorf("container network settings are missing")
	}

	networks := containerInfo.NetworkSettings.Networks
	if endpoint := networks[constants.DockerNetwork]; endpoint != nil {
		if endpoint.IPAddress != "" {
			return endpoint.IPAddress, nil
		}
		if endpoint.GlobalIPv6Address != "" {
			return endpoint.GlobalIPv6Address, nil
		}
	}

	var candidates []string
	for _, endpoint := range networks {
		if endpoint == nil {
			continue
		}
		if endpoint.IPAddress != "" {
			candidates = append(candidates, endpoint.IPAddress)
			continue
		}
		if endpoint.GlobalIPv6Address != "" {
			candidates = append(candidates, endpoint.GlobalIPv6Address)
		}
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("container has no usable network address")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("container is attached to multiple non-Haloy networks")
	}
}
