package api

import (
	"context"
	"fmt"
	"net"
	"sync"
)

const (
	defaultMaxTunnels          = 256
	defaultMaxTunnelsPerClient = 64
)

// TunnelPolicy controls the amount and scope of TCP tunnel access.
type TunnelPolicy struct {
	MaxOpen                      int
	MaxOpenPerClient             int
	AllowHostNetworkPortOverride bool
}

func DefaultTunnelPolicy() TunnelPolicy {
	return TunnelPolicy{
		MaxOpen:          defaultMaxTunnels,
		MaxOpenPerClient: defaultMaxTunnelsPerClient,
	}
}

func (p TunnelPolicy) withDefaults() TunnelPolicy {
	if p.MaxOpen <= 0 {
		p.MaxOpen = defaultMaxTunnels
	}
	if p.MaxOpenPerClient <= 0 {
		p.MaxOpenPerClient = defaultMaxTunnelsPerClient
	}
	if p.MaxOpenPerClient > p.MaxOpen {
		p.MaxOpenPerClient = p.MaxOpen
	}
	return p
}

type tunnelCapacity struct {
	mu       sync.Mutex
	policy   TunnelPolicy
	total    int
	byClient map[string]int
}

func newTunnelCapacity(policy TunnelPolicy) *tunnelCapacity {
	return &tunnelCapacity{
		policy:   policy.withDefaults(),
		byClient: make(map[string]int),
	}
}

func (c *tunnelCapacity) acquire(client string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.total >= c.policy.MaxOpen || c.byClient[client] >= c.policy.MaxOpenPerClient {
		return false
	}
	c.total++
	c.byClient[client]++
	return true
}

func (c *tunnelCapacity) release(client string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.total > 0 {
		c.total--
	}
	if c.byClient[client] <= 1 {
		delete(c.byClient, client)
	} else {
		c.byClient[client]--
	}
}

// connectionTracker accounts for connections hidden from http.Server after a
// handler calls Hijack.
type connectionTracker struct {
	mu           sync.Mutex
	conns        map[net.Conn]struct{}
	wg           sync.WaitGroup
	shuttingDown bool
	groups       int
}

func newConnectionTracker() *connectionTracker {
	return &connectionTracker{conns: make(map[net.Conn]struct{})}
}

func (t *connectionTracker) track(conns ...net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.shuttingDown {
		return false
	}
	for _, conn := range conns {
		t.conns[conn] = struct{}{}
	}
	t.groups++
	t.wg.Add(1)
	return true
}

func (t *connectionTracker) untrack(conns ...net.Conn) {
	t.mu.Lock()
	removed := false
	for _, conn := range conns {
		if _, ok := t.conns[conn]; !ok {
			continue
		}
		delete(t.conns, conn)
		removed = true
	}
	if removed {
		if t.groups > 0 {
			t.groups--
		}
		t.wg.Done()
	}
	t.mu.Unlock()
}

func (t *connectionTracker) shutdown(ctx context.Context) error {
	t.mu.Lock()
	t.shuttingDown = true
	t.mu.Unlock()

	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		t.mu.Lock()
		open := t.groups
		for conn := range t.conns {
			_ = conn.Close()
		}
		t.mu.Unlock()
		<-done
		return fmt.Errorf("force-closed %d tunnel connection(s): %w", open, ctx.Err())
	}
}
