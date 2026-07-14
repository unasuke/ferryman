package forwarder

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// discoverInterval is how often the remote is polled for new listen ports.
const discoverInterval = 2 * time.Second

// RemoteListener is a TCP port found listening on the remote host.
type RemoteListener struct {
	Port    string // e.g. "3000"
	Process string // best-effort from `ss -p`, may be ""
}

// Suggestion is a newly-appeared remote listener not already covered by a rule.
type Suggestion struct {
	Port    string
	Process string
}

// discover polls the remote host for listening TCP ports and emits a Suggestion
// for each newly-appeared port that no rule already forwards. The first probe is
// a silent baseline: only ports that show up after the connection is established
// are suggested. It runs for the life of one connection (ctx is cancelled on
// drop). If probing fails (no ss, non-Linux remote, ...) it returns quietly
// without emitting anything, so a working connection is never reported as broken.
func (m *Manager) discover(ctx context.Context, client *ssh.Client) {
	prev, err := snapshot(ctx, client)
	if err != nil {
		return // ss unavailable: give up on discovery for this connection
	}
	t := time.NewTicker(discoverInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur, err := snapshot(ctx, client)
			if err != nil {
				return
			}
			for _, s := range newPorts(prev, cur, m.hasRuleForRemotePort) {
				m.emitSuggestion(s)
			}
			prev = cur
		}
	}
}

// snapshot probes the remote listeners and returns them keyed by port.
func snapshot(ctx context.Context, client *ssh.Client) (map[string]RemoteListener, error) {
	listeners, err := probeListeners(ctx, client)
	if err != nil {
		return nil, err
	}
	out := make(map[string]RemoteListener, len(listeners))
	for _, l := range listeners {
		out[l.Port] = l
	}
	return out, nil
}

// newPorts returns a Suggestion for each listener present in cur but not in prev
// whose port is not already forwarded by a rule (hasRule reports that). It is a
// pure function so the diff/filter logic can be tested without running ss.
func newPorts(prev, cur map[string]RemoteListener, hasRule func(port string) bool) []Suggestion {
	var out []Suggestion
	for port, l := range cur {
		if _, seen := prev[port]; seen {
			continue
		}
		if hasRule(port) {
			continue
		}
		out = append(out, Suggestion{Port: l.Port, Process: l.Process})
	}
	return out
}

// probeListeners runs ss on the remote and parses its output. It goes through a
// login shell (`sh -lc`) so ss is found even when it lives in /usr/sbin or /sbin
// (a non-interactive SSH exec would otherwise get a minimal PATH).
func probeListeners(ctx context.Context, client *ssh.Client) ([]RemoteListener, error) {
	out, err := runCommand(ctx, client, "sh -lc 'ss -Htlnp'")
	if err != nil {
		return nil, err
	}
	return parseSS(out), nil
}

// runCommand runs cmd on the remote and returns its stdout. stderr is ignored:
// `ss -p` warns there when it cannot read other users' process info without
// privileges, which is harmless. The command is aborted if ctx is cancelled.
func runCommand(ctx context.Context, client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdout = &buf
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		sess.Close()
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return "", err
		}
		return buf.String(), nil
	}
}

// parseSS turns `ss -Htlnp` output into a de-duplicated list of listeners. Each
// line looks like:
//
//	LISTEN 0 511  *:3000  *:*  users:(("node",pid=1234,fd=18))
//
// The 4th field is the local address:port; the port is the text after its last
// colon. Non-numeric ports and short lines are skipped. Ports are de-duplicated
// (an IPv4 and IPv6 listener on the same port collapse to one). The process name
// is extracted best-effort from the users:(("NAME",...)) column.
func parseSS(out string) []RemoteListener {
	var res []RemoteListener
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port := local[i+1:]
		if _, err := strconv.Atoi(port); err != nil {
			continue
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		res = append(res, RemoteListener{Port: port, Process: parseProcess(line)})
	}
	return res
}

// parseProcess extracts the first process name from an ss users:(("NAME",...))
// column, or "" if the column is absent.
func parseProcess(line string) string {
	const marker = `users:(("`
	i := strings.Index(line, marker)
	if i < 0 {
		return ""
	}
	rest := line[i+len(marker):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}
