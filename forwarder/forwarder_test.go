package forwarder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startEchoServer runs a loopback TCP echo server and returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { io.Copy(c, c); c.Close() }(c)
		}
	}()
	return ln.Addr().String()
}

// directTCPIP is the RFC 4254 "direct-tcpip" channel open payload.
type directTCPIP struct {
	DestAddr string
	DestPort uint32
	SrcAddr  string
	SrcPort  uint32
}

// startSSHServer runs an in-process SSH server that accepts any client (no auth)
// and bridges each direct-tcpip channel to the requested address. It returns the
// listen address.
func startSSHServer(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(c, cfg)
		}
	}()
	return ln.Addr().String()
}

func serveSSHConn(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	_ = conn
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var m directTCPIP
		if err := ssh.Unmarshal(nc.ExtraData(), &m); err != nil {
			nc.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		dst, err := net.Dial("tcp", net.JoinHostPort(m.DestAddr, strconv.Itoa(int(m.DestPort))))
		if err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			dst.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go bridge(ch, dst)
	}
}

func bridge(ch ssh.Channel, dst net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(ch, dst); done <- struct{}{} }()
	go func() { io.Copy(dst, ch); done <- struct{}{} }()
	<-done
	ch.Close()
	dst.Close()
}

// testDialer connects to the in-process SSH server, skipping host key and auth
// checks (test-only).
func testDialer(serverAddr string) Dialer {
	return func(ctx context.Context) (*ssh.Client, error) {
		conn, err := net.Dial("tcp", serverAddr)
		if err != nil {
			return nil, err
		}
		cc, chans, reqs, err := ssh.NewClientConn(conn, serverAddr, &ssh.ClientConfig{
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		if err != nil {
			conn.Close()
			return nil, err
		}
		return ssh.NewClient(cc, chans, reqs), nil
	}
}

// freeLocalAddr returns a currently-free loopback address to bind a forward to.
func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// dialWithRetry dials addr until it succeeds or timeout elapses. Used to wait
// for a forward's listener to come up without depending on event ordering.
func dialWithRetry(addr string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			return c, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, lastErr
}

// waitForClosed reports whether addr stops accepting connections within timeout.
func waitForClosed(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return true
		}
		c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// assertEcho dials addr (with retry) and verifies a byte round-trip through the
// forward to the echo server.
func assertEcho(t *testing.T, addr string) {
	t.Helper()
	c, err := dialWithRetry(addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	msg := []byte("ping\n")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}
}

func TestManagerForwardsRoundTrip(t *testing.T) {
	echoAddr := startEchoServer(t)
	sshAddr := startSSHServer(t)
	local := freeLocalAddr(t)

	m := New(testDialer(sshAddr))
	m.SetRules([]Rule{{Name: "echo", LocalAddr: local, RemoteAddr: echoAddr, Enabled: true}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	assertEcho(t, local)
}

func TestManagerLiveReconcile(t *testing.T) {
	echoAddr := startEchoServer(t)
	sshAddr := startSSHServer(t)
	local1 := freeLocalAddr(t)
	local2 := freeLocalAddr(t)

	m := New(testDialer(sshAddr))
	m.SetRules([]Rule{{Name: "a", LocalAddr: local1, RemoteAddr: echoAddr, Enabled: true}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	assertEcho(t, local1)

	// Deleting the rule stops its listener.
	m.DeleteRule(local1)
	if !waitForClosed(local1, 2*time.Second) {
		t.Fatalf("listener on %s still accepting after DeleteRule", local1)
	}

	// Adding a new rule on a live connection starts a working forward.
	m.UpsertRule(Rule{Name: "b", LocalAddr: local2, RemoteAddr: echoAddr, Enabled: true})
	assertEcho(t, local2)
}

func TestStateString(t *testing.T) {
	cases := map[State]string{
		StateStopped:    "stopped",
		StateConnecting: "connecting",
		StateRunning:    "running",
		StateError:      "error",
		State(99):       "stopped", // unknown/default
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

func findRule(rules []Rule, localAddr string) *Rule {
	for i := range rules {
		if rules[i].LocalAddr == localAddr {
			return &rules[i]
		}
	}
	return nil
}

func TestManagerRuleCRUD(t *testing.T) {
	m := New(nil) // dialer unused: Run is never called

	m.SetRules([]Rule{
		{LocalAddr: "127.0.0.1:1", RemoteAddr: "a:1", Enabled: true},
		{LocalAddr: "127.0.0.1:2", RemoteAddr: "b:2"},
	})
	if got := len(m.Rules()); got != 2 {
		t.Fatalf("after SetRules len = %d, want 2", got)
	}

	// Upsert on an existing key updates in place; a new key appends.
	m.UpsertRule(Rule{LocalAddr: "127.0.0.1:1", RemoteAddr: "a:99", Enabled: false})
	m.UpsertRule(Rule{LocalAddr: "127.0.0.1:3", RemoteAddr: "c:3", Enabled: true})
	rules := m.Rules()
	if len(rules) != 3 {
		t.Fatalf("after upsert len = %d, want 3", len(rules))
	}
	if r := findRule(rules, "127.0.0.1:1"); r == nil || r.RemoteAddr != "a:99" || r.Enabled {
		t.Errorf("upsert-update wrong: %+v", r)
	}

	m.DeleteRule("127.0.0.1:2")
	if findRule(m.Rules(), "127.0.0.1:2") != nil {
		t.Error("DeleteRule did not remove the rule")
	}
	if got := len(m.Rules()); got != 2 {
		t.Errorf("after delete len = %d, want 2", got)
	}

	// Rules() returns a copy: mutating it must not affect the manager.
	out := m.Rules()
	out[0].RemoteAddr = "mutated"
	if findRule(m.Rules(), out[0].LocalAddr).RemoteAddr == "mutated" {
		t.Error("Rules() did not return a copy")
	}
}

func TestEmitNonBlocking(t *testing.T) {
	m := New(nil)
	// Emit far past the 64-slot buffer with no consumer; this must not block
	// (a block would hang until the test timeout).
	for i := 0; i < 100; i++ {
		m.emit(Event{State: StateRunning})
	}
	// The buffer holds 64; the overflow must have been dropped.
	n := 0
	for {
		select {
		case <-m.Events():
			n++
		default:
			if n != 64 {
				t.Fatalf("drained %d events, want 64 (buffer cap)", n)
			}
			return
		}
	}
}

func TestNextBackoff(t *testing.T) {
	in := []time.Duration{1, 2, 4, 8, 16, 30}
	want := []time.Duration{2, 4, 8, 16, 30, 30}
	for i := range in {
		if got := nextBackoff(in[i] * time.Second); got != want[i]*time.Second {
			t.Errorf("nextBackoff(%v) = %v, want %v", in[i]*time.Second, got, want[i]*time.Second)
		}
	}
}
