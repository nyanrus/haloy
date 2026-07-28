package haloy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/haloydev/haloy/internal/apiclient"
	"github.com/haloydev/haloy/internal/config"
	"github.com/haloydev/haloy/internal/configloader"
	"github.com/haloydev/haloy/internal/constants"
	"github.com/haloydev/haloy/internal/helpers"
	"github.com/haloydev/haloy/internal/ui"
	"github.com/spf13/cobra"
)

const (
	tunnelHandshakeTimeout    = 15 * time.Second
	maxTunnelErrorBodyBytes   = 64 << 10
	maxTunnelHeaderBytes      = 64 << 10
	maxLocalTunnelConnections = 64
)

func TunnelCmd(configPath *string, flags *appCmdFlags) *cobra.Command {
	var (
		localPortFlag  string
		remotePortFlag string
		containerID    string
	)

	cmd := &cobra.Command{
		Use:   "tunnel [local-port]",
		Short: "Create a TCP tunnel to a container",
		Long: `Create a TCP tunnel to a running container, allowing local connections to be
forwarded to the container's port.

If local-port and --port are omitted, the local port defaults to the port configured
for the target in haloy.yaml. The remote port also defaults to the configured port.
Use --remote-port to override the container port.

The listener is loopback-only, but any process running on the local machine can
connect to it while the tunnel is active.

Examples:
  # Tunnel to postgres (uses port 5432 from config for both local and remote)
  haloy tunnel -t postgres
  # Then connect: psql -h localhost -p 5432

  # Use a different local port
  haloy tunnel -t postgres --port 15432
  # Then connect: psql -h localhost -p 15432

  # Use a different local port with the positional form
  haloy tunnel 15432 -t postgres

  # Use a different remote container port
  haloy tunnel -t postgres --port 15432 --remote-port 5432

  # Tunnel to a specific container (for apps with replicas)
  haloy tunnel --container abc123`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			rawDeployConfig, format, err := configloader.Load(ctx, *configPath, flags.targets, flags.all)
			if err != nil {
				return fmt.Errorf("unable to load config: %w", err)
			}

			resolvedDeployConfig, err := configloader.ResolveSecrets(ctx, rawDeployConfig, *configPath)
			if err != nil {
				return fmt.Errorf("unable to resolve secrets: %w", err)
			}

			targets, err := configloader.ExtractTargets(resolvedDeployConfig, format)
			if err != nil {
				return err
			}

			if len(targets) != 1 {
				return fmt.Errorf("tunnel requires exactly one target, got %d (use --targets to specify)", len(targets))
			}

			var target config.TargetConfig
			for _, t := range targets {
				target = t
				break
			}

			localPort, remotePort, err := resolveTunnelPorts(target, args, localPortFlag, remotePortFlag)
			if err != nil {
				return err
			}

			return runTunnel(ctx, &target, localPort, remotePort, containerID)
		},
	}

	cmd.Flags().StringVarP(&flags.configPath, "config", "c", "", "Path to config file or directory (default: .)")
	cmd.Flags().StringSliceVarP(&flags.targets, "targets", "t", nil, "Target to tunnel to (required for multi-target configs)")
	cmd.Flags().StringVar(&localPortFlag, "port", "", "Local port to listen on (default: port from config)")
	cmd.Flags().StringVar(&remotePortFlag, "remote-port", "", "Remote container port to tunnel to (default: port from config)")
	cmd.Flags().StringVar(&containerID, "container", "", "Specific container ID to tunnel to")

	cmd.RegisterFlagCompletionFunc("targets", completeTargetNames)

	return cmd
}

func resolveTunnelPorts(target config.TargetConfig, args []string, localPortFlag, remotePortFlag string) (string, string, error) {
	if len(args) > 0 && localPortFlag != "" {
		return "", "", fmt.Errorf("local port specified twice: use either [local-port] or --port")
	}

	var localPort string
	switch {
	case localPortFlag != "":
		localPort = localPortFlag
	case len(args) > 0:
		localPort = args[0]
	case target.Port != "":
		localPort = target.Port.String()
	default:
		return "", "", fmt.Errorf("no port configured for target %q; specify local port as argument or with --port", target.Name)
	}

	if err := helpers.ValidatePort(localPort); err != nil {
		return "", "", fmt.Errorf("invalid local port: %w", err)
	}

	if remotePortFlag != "" {
		if err := helpers.ValidatePort(remotePortFlag); err != nil {
			return "", "", fmt.Errorf("invalid remote port: %w", err)
		}
	}

	return localPort, remotePortFlag, nil
}

func runTunnel(ctx context.Context, targetConfig *config.TargetConfig, localPort, remotePort, containerID string) error {
	token, err := getToken(targetConfig, targetConfig.Server)
	if err != nil {
		return fmt.Errorf("unable to get token: %w", err)
	}

	api, err := apiclient.New(targetConfig.Server, token)
	if err != nil {
		return fmt.Errorf("unable to create API client: %w", err)
	}

	if err := api.HealthCheck(ctx); err != nil {
		return fmt.Errorf("server not available: %w", err)
	}

	normalizedURL, err := helpers.NormalizeServerURL(targetConfig.Server)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}

	// Determine TLS based on localhost check (same logic as BuildServerURL)
	useTLS := !helpers.IsLocalhost(normalizedURL)

	host, err := tunnelServerAddress(normalizedURL, useTLS)
	if err != nil {
		return fmt.Errorf("invalid server address: %w", err)
	}

	// Check if port is already in use
	listenAddr := net.JoinHostPort("127.0.0.1", localPort)
	checkConn, err := net.DialTimeout("tcp", listenAddr, 100*time.Millisecond)
	if err == nil {
		checkConn.Close()
		return fmt.Errorf("port %s is already in use", localPort)
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}
	defer listener.Close()

	stopListener := context.AfterFunc(ctx, func() {
		_ = listener.Close()
	})
	defer stopListener()

	ui.Info("Tunnel listening on 127.0.0.1:%s -> %s", localPort, targetConfig.Name)
	ui.Info("Press Ctrl+C to stop")

	slots := make(chan struct{}, maxLocalTunnelConnections)
	var handlers sync.WaitGroup

	// Accept connections
	for {
		localConn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				handlers.Wait()
				return nil
			default:
				ui.Warn("Failed to accept connection: %v", err)
				continue
			}
		}

		select {
		case slots <- struct{}{}:
		default:
			_ = localConn.Close()
			ui.Warn("Tunnel connection limit reached (%d)", maxLocalTunnelConnections)
			continue
		}

		// Handle each connection in a goroutine
		handlers.Add(1)
		go func(local net.Conn) {
			defer handlers.Done()
			defer func() { <-slots }()
			defer local.Close()

			// Establish tunnel to server
			remote, err := dialTunnel(ctx, host, useTLS, token, targetConfig.Name, remotePort, containerID)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				ui.Error("Failed to establish tunnel: %v", err)
				return
			}
			defer remote.Close()

			stopConnections := context.AfterFunc(ctx, func() {
				_ = local.Close()
				_ = remote.Close()
			})
			defer stopConnections()

			// Bidirectional copy
			var wg sync.WaitGroup
			wg.Add(2)

			// Local -> Remote
			go func() {
				defer wg.Done()
				io.Copy(remote, local)
				if tc, ok := remote.(*tunnelConn); ok {
					if tcpConn, ok := tc.closer.(*net.TCPConn); ok {
						tcpConn.CloseWrite()
					}
				} else if tcpConn, ok := remote.(*net.TCPConn); ok {
					tcpConn.CloseWrite()
				}
			}()

			// Remote -> Local
			go func() {
				defer wg.Done()
				io.Copy(local, remote)
				if tcpConn, ok := local.(*net.TCPConn); ok {
					tcpConn.CloseWrite()
				}
			}()

			wg.Wait()
		}(localConn)
	}
}

func tunnelServerAddress(host string, useTLS bool) (string, error) {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, nil
	}

	hostname := strings.Trim(host, "[]")
	if hostname == "" || net.ParseIP(hostname) == nil && strings.Contains(hostname, ":") {
		return "", fmt.Errorf("invalid host %q", host)
	}

	port := "80"
	if useTLS {
		port = "443"
	}
	return net.JoinHostPort(hostname, port), nil
}

// dialTunnel establishes a TCP tunnel to a container through the API server.
// host should include the port (e.g., "example.com:443").
// It returns a net.Conn that can be used to communicate with the container.
func dialTunnel(ctx context.Context, host string, useTLS bool, token, appName, remotePort, containerID string) (net.Conn, error) {
	requestURL := &url.URL{
		Scheme: "http",
		Host:   host,
		Path:   "/v1/tunnel/" + appName,
	}
	if useTLS {
		requestURL.Scheme = "https"
	}
	params := requestURL.Query()
	if remotePort != "" {
		params.Set("port", remotePort)
	}
	if containerID != "" {
		params.Set("container", containerID)
	}
	requestURL.RawQuery = params.Encode()

	dialCtx, cancel := context.WithTimeout(ctx, tunnelHandshakeTimeout)
	defer cancel()

	dialer := &net.Dialer{}
	var (
		conn net.Conn
		err  error
	)
	if useTLS {
		tlsDialer := &tls.Dialer{
			NetDialer: dialer,
			Config: &tls.Config{
				MinVersion: tls.VersionTLS12,
				NextProtos: []string{"http/1.1"},
			},
		}
		conn, err = tlsDialer.DialContext(dialCtx, "tcp", host)
	} else {
		conn, err = dialer.DialContext(dialCtx, "tcp", host)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(tunnelHandshakeTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to set handshake deadline: %w", err)
	}

	req, err := http.NewRequestWithContext(dialCtx, http.MethodPost, requestURL.String(), nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create tunnel request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")

	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	limitedReader := &tunnelHandshakeReader{reader: conn, remaining: maxTunnelHeaderBytes, limited: true}
	reader := bufio.NewReader(limitedReader)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	limitedReader.disableLimit()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxTunnelErrorBodyBytes+1))
		_ = resp.Body.Close()
		conn.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("authentication failed - check your %s", constants.EnvVarAPIToken)
		}
		if readErr != nil {
			return nil, fmt.Errorf("tunnel request failed with status %d (unable to read error: %w)", resp.StatusCode, readErr)
		}
		truncated := len(body) > maxTunnelErrorBodyBytes
		if truncated {
			body = body[:maxTunnelErrorBodyBytes]
		}
		message := strings.TrimSpace(string(body))
		if truncated {
			message += "…"
		}
		return nil, fmt.Errorf("tunnel request failed with status %d: %s", resp.StatusCode, message)
	}

	if !strings.EqualFold(resp.Header.Get("Upgrade"), "tcp") ||
		!headerContainsToken(resp.Header.Get("Connection"), "upgrade") {
		_ = resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("invalid tunnel upgrade response")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to clear handshake deadline: %w", err)
	}

	if reader.Buffered() > 0 {
		return &tunnelConn{
			reader:   reader,
			writer:   conn,
			closer:   conn,
			respBody: resp.Body,
		}, nil
	}

	_ = resp.Body.Close()
	return conn, nil
}

func headerContainsToken(value, token string) bool {
	for part := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

type tunnelHandshakeReader struct {
	reader    io.Reader
	remaining int64
	limited   bool
}

func (r *tunnelHandshakeReader) Read(p []byte) (int, error) {
	if !r.limited {
		return r.reader.Read(p)
	}
	if r.remaining <= 0 {
		return 0, fmt.Errorf("tunnel response headers exceed %d bytes", maxTunnelHeaderBytes)
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *tunnelHandshakeReader) disableLimit() {
	r.limited = false
}

// tunnelConn wraps the HTTP response body for reading and the underlying connection for writing.
// This is necessary because the HTTP client may have buffered data in resp.Body that we need to read.
type tunnelConn struct {
	reader   io.Reader
	writer   io.Writer
	closer   io.Closer
	respBody io.Closer
}

func (t *tunnelConn) Read(p []byte) (int, error) {
	return t.reader.Read(p)
}

func (t *tunnelConn) Write(p []byte) (int, error) {
	return t.writer.Write(p)
}

func (t *tunnelConn) Close() error {
	if t.respBody != nil {
		t.respBody.Close()
	}
	return t.closer.Close()
}

// Implement net.Conn interface methods that we need
func (t *tunnelConn) LocalAddr() net.Addr {
	if conn, ok := t.closer.(net.Conn); ok {
		return conn.LocalAddr()
	}
	return nil
}

func (t *tunnelConn) RemoteAddr() net.Addr {
	if conn, ok := t.closer.(net.Conn); ok {
		return conn.RemoteAddr()
	}
	return nil
}

func (t *tunnelConn) SetDeadline(deadline time.Time) error {
	if conn, ok := t.closer.(net.Conn); ok {
		return conn.SetDeadline(deadline)
	}
	return nil
}

func (t *tunnelConn) SetReadDeadline(deadline time.Time) error {
	if conn, ok := t.closer.(net.Conn); ok {
		return conn.SetReadDeadline(deadline)
	}
	return nil
}

func (t *tunnelConn) SetWriteDeadline(deadline time.Time) error {
	if conn, ok := t.closer.(net.Conn); ok {
		return conn.SetWriteDeadline(deadline)
	}
	return nil
}
