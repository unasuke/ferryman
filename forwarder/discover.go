package forwarder

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// discoverInterval is how often the remote is polled for new listen ports.
	discoverInterval = 1500 * time.Millisecond
	// requiredScans is how many consecutive scans a newly-appeared port must
	// stay listening before it is suggested. Ports that come and go within a
	// single scan gap (a browser started by a Rails system spec, say) never
	// reach it, so they are not suggested. 1 would suggest on first sight.
	requiredScans = 2
)

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

// discover polls the remote host for listening TCP ports and emits a
// Suggestion for each newly-appeared port that stays listening for
// requiredScans consecutive scans and that no rule already forwards. The
// first probe is a silent baseline: only ports that show up after the
// connection is established are considered, and a port must survive the
// confirmation to be suggested, so a listener that comes and goes between
// scans (a browser started by a Rails system spec, say) is never reported.
// It runs for the life of one connection (ctx is cancelled on drop). If
// probing fails (no ss, non-Linux remote, ...) it returns quietly without
// emitting anything, so a working connection is never reported as broken.
func (m *Manager) discover(ctx context.Context, client *ssh.Client) {
	prev, err := snapshot(ctx, client)
	if err != nil {
		return // ss unavailable: give up on discovery for this connection
	}
	streak := map[string]int{}
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
			streak = bumpStreaks(streak, prev, cur, requiredScans)
			for _, s := range confirmedPorts(streak, cur, requiredScans, m.hasRuleForRemotePort) {
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

// bumpStreaks folds one scan into the per-port consecutive-detection counts.
// A port in cur but not in prev starts a fresh streak at 1; a port continuing
// from prev keeps counting only if it already had a streak, so ports that were
// already listening at the baseline stay uncounted and are never suggested.
// Ports gone from cur are dropped, so a port that closes and reopens starts
// over. Counts stop at need+1: past that the exact value carries no meaning,
// and clamping keeps "candidate" (1..need-1), "just ripened" (need) and
// "already suggested" (need+1) readable.
func bumpStreaks(streak map[string]int, prev, cur map[string]RemoteListener, need int) map[string]int {
	next := make(map[string]int, len(cur))
	for port := range cur {
		if _, seen := prev[port]; !seen {
			next[port] = 1
			continue
		}
		if n := streak[port]; n > 0 {
			next[port] = min(n, need) + 1
		}
	}
	return next
}

// confirmedPorts returns a Suggestion for each listener whose streak has just
// reached need, skipping ports a rule already forwards (hasRule reports that).
// The comparison is exact so a long-lived port is suggested once, not on every
// scan after it ripens.
func confirmedPorts(streak map[string]int, cur map[string]RemoteListener, need int, hasRule func(port string) bool) []Suggestion {
	var out []Suggestion
	for port, l := range cur {
		if streak[port] != need {
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
