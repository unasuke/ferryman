// Package forwarder implements in-process SSH local port forwarding.
//
// It owns a single SSH connection and a set of local->remote forward rules,
// reconnects automatically, and re-establishes enabled forwards after a drop.
// Because it speaks SSH in-process (golang.org/x/crypto/ssh) rather than
// shelling out to the ssh binary, it needs no ControlMaster and builds to a
// single static binary on Windows, macOS and Linux.
package forwarder

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Rule is a single local->remote port forward.
type Rule struct {
	Name string `json:"name"`
	// LocalAddr is the host-side bind, e.g. "127.0.0.1:8080".
	LocalAddr string `json:"local_addr"`
	// RemoteAddr is resolved on the remote host, e.g. "localhost:3000".
	// Because it is resolved remotely, dev servers bound only to the remote
	// loopback (127.0.0.1 or ::1) are reachable without exposing them.
	RemoteAddr string `json:"remote_addr"`
	Enabled    bool   `json:"enabled"`
}

func (r Rule) key() string { return r.LocalAddr }

// State describes connection- or forward-level status.
type State int

const (
	StateStopped State = iota
	StateConnecting
	StateRunning
	StateError
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateRunning:
		return "running"
	case StateError:
		return "error"
	default:
		return "stopped"
	}
}

// Event is emitted to the UI on any status change. Rule is the local address
// of the affected forward, or "" for connection-level events.
type Event struct {
	Rule  string
	State State
	Err   error
}

// Dialer establishes a new SSH client. It is called on every (re)connect.
type Dialer func(ctx context.Context) (*ssh.Client, error)

type forward struct {
	rule     Rule
	listener net.Listener
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func (f *forward) track(c net.Conn) {
	f.mu.Lock()
	f.conns[c] = struct{}{}
	f.mu.Unlock()
}

func (f *forward) untrack(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
}

func (f *forward) close() {
	f.listener.Close()
	f.mu.Lock()
	for c := range f.conns {
		c.Close()
	}
	f.conns = map[net.Conn]struct{}{}
	f.mu.Unlock()
}

// Manager owns the SSH connection and the forward set.
type Manager struct {
	dialer Dialer
	events chan Event

	mu     sync.Mutex
	client *ssh.Client
	rules  []Rule
	active map[string]*forward
}

// New returns a Manager that will connect using dialer when Run is called.
func New(dialer Dialer) *Manager {
	return &Manager{
		dialer: dialer,
		events: make(chan Event, 64),
		active: map[string]*forward{},
	}
}

// Events returns the status channel. Drain it from the UI.
func (m *Manager) Events() <-chan Event { return m.events }

func (m *Manager) emit(e Event) {
	select {
	case m.events <- e:
	default: // never block the engine on a slow consumer
	}
}

// Rules returns a copy of the current rule set.
func (m *Manager) Rules() []Rule {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Rule, len(m.rules))
	copy(out, m.rules)
	return out
}

// SetRules replaces the rule set and reconciles active forwards.
func (m *Manager) SetRules(rules []Rule) {
	m.mu.Lock()
	m.rules = append([]Rule(nil), rules...)
	m.mu.Unlock()
	m.reconcile()
}

// UpsertRule adds or updates a rule, keyed by local address.
func (m *Manager) UpsertRule(r Rule) {
	m.mu.Lock()
	found := false
	for i := range m.rules {
		if m.rules[i].key() == r.key() {
			m.rules[i] = r
			found = true
			break
		}
	}
	if !found {
		m.rules = append(m.rules, r)
	}
	m.mu.Unlock()
	m.reconcile()
}

// DeleteRule removes a rule by local address.
func (m *Manager) DeleteRule(localAddr string) {
	m.mu.Lock()
	out := m.rules[:0]
	for _, r := range m.rules {
		if r.key() != localAddr {
			out = append(out, r)
		}
	}
	m.rules = out
	m.mu.Unlock()
	m.reconcile()
}

// Run connects and keeps the connection alive, reconnecting with backoff,
// until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		m.emit(Event{State: StateConnecting})
		client, err := m.dialer(ctx)
		if err != nil {
			m.emit(Event{State: StateError, Err: err})
			if !sleep(ctx, backoff) {
				break
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second

		m.mu.Lock()
		m.client = client
		m.mu.Unlock()
		m.emit(Event{State: StateRunning})
		m.reconcile() // start enabled forwards on the fresh client

		err = keepalive(ctx, client)

		m.stopAllForwards()
		m.mu.Lock()
		m.client = nil
		m.mu.Unlock()
		client.Close()
		m.emit(Event{State: StateStopped, Err: err})

		if ctx.Err() != nil {
			break
		}
		if !sleep(ctx, backoff) {
			break
		}
	}
	m.stopAllForwards()
}

func keepalive(ctx context.Context, c *ssh.Client) error {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	closed := make(chan error, 1)
	go func() { closed <- c.Wait() }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-closed:
			return err
		case <-t.C:
			if _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return err
			}
		}
	}
}

// reconcile makes the running forwards match the enabled rules.
func (m *Manager) reconcile() {
	m.mu.Lock()
	connected := m.client != nil
	desired := map[string]Rule{}
	for _, r := range m.rules {
		if r.Enabled {
			desired[r.key()] = r
		}
	}
	current := map[string]string{}
	for k, f := range m.active {
		current[k] = f.rule.RemoteAddr
	}
	m.mu.Unlock()

	if !connected {
		return
	}
	// Stop forwards no longer desired, or whose target changed.
	for k, remote := range current {
		if d, ok := desired[k]; !ok || d.RemoteAddr != remote {
			m.stopForward(k)
		}
	}
	// Start newly desired forwards.
	for k, r := range desired {
		m.mu.Lock()
		_, running := m.active[k]
		m.mu.Unlock()
		if !running {
			m.startForward(r)
		}
	}
}

func (m *Manager) startForward(r Rule) {
	ln, err := net.Listen("tcp", r.LocalAddr)
	if err != nil {
		m.emit(Event{Rule: r.LocalAddr, State: StateError, Err: err})
		return
	}
	f := &forward{rule: r, listener: ln, conns: map[net.Conn]struct{}{}}
	m.mu.Lock()
	m.active[r.key()] = f
	m.mu.Unlock()
	m.emit(Event{Rule: r.LocalAddr, State: StateRunning})
	go m.accept(f)
}

func (m *Manager) accept(f *forward) {
	for {
		local, err := f.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go m.handle(f, local)
	}
}

func (m *Manager) handle(f *forward, local net.Conn) {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()
	if client == nil {
		local.Close()
		return
	}
	// "localhost:3000" here is resolved on the remote side by sshd.
	remote, err := client.Dial("tcp", f.rule.RemoteAddr)
	if err != nil {
		m.emit(Event{Rule: f.rule.LocalAddr, State: StateError, Err: err})
		local.Close()
		return
	}
	f.track(local)
	f.track(remote)
	defer func() {
		f.untrack(local)
		f.untrack(remote)
		local.Close()
		remote.Close()
	}()
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()
	<-done
}

func (m *Manager) stopForward(localAddr string) {
	m.mu.Lock()
	f := m.active[localAddr]
	delete(m.active, localAddr)
	m.mu.Unlock()
	if f != nil {
		f.close()
		m.emit(Event{Rule: localAddr, State: StateStopped})
	}
}

func (m *Manager) stopAllForwards() {
	m.mu.Lock()
	fs := m.active
	m.active = map[string]*forward{}
	m.mu.Unlock()
	for k, f := range fs {
		f.close()
		m.emit(Event{Rule: k, State: StateStopped})
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d *= 2; d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}
