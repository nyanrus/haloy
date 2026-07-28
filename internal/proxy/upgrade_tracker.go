package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
)

var errProxyShuttingDown = errors.New("proxy is shutting down")

type trackedUpgradeWriter struct {
	http.ResponseWriter
	proxy *Proxy
}

func (w *trackedUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, brw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	if !w.proxy.trackHijacked(conn) {
		_ = conn.Close()
		return nil, nil, errProxyShuttingDown
	}
	return &trackedHijackedConn{
		Conn: conn,
		onClose: func() {
			w.proxy.untrackHijacked(conn)
		},
	}, brw, nil
}

func (w *trackedUpgradeWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type trackedHijackedConn struct {
	net.Conn
	closeOnce sync.Once
	onClose   func()
}

func (c *trackedHijackedConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(c.onClose)
	return err
}

func requestIsTCPUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "tcp") {
		return false
	}
	for part := range strings.SplitSeq(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
			return true
		}
	}
	return false
}
