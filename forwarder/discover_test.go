package forwarder

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseSS(t *testing.T) {
	// Header suppressed by -H; process column present via -p on some rows.
	out := "" +
		"LISTEN 0      128        127.0.0.1:6379       0.0.0.0:*    users:((\"redis-server\",pid=800,fd=6))\n" +
		"LISTEN 0      511                *:3000             *:*     users:((\"node\",pid=1234,fd=18))\n" +
		"LISTEN 0      511             [::1]:3000            [::]:*   users:((\"node\",pid=1234,fd=20))\n" +
		"LISTEN 0      4096   127.0.0.53%lo:53          0.0.0.0:*\n" +
		"garbage line\n" +
		"\n"

	got := parseSS(out)
	want := []RemoteListener{
		{Port: "6379", Process: "redis-server"},
		{Port: "3000", Process: "node"}, // IPv4/IPv6 on 3000 collapse to one
		{Port: "53", Process: ""},       // no process column
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSS =\n %+v\nwant\n %+v", got, want)
	}
}

func TestParseSSEmpty(t *testing.T) {
	if got := parseSS(""); len(got) != 0 {
		t.Fatalf("parseSS(\"\") = %+v, want empty", got)
	}
}

func TestNewPorts(t *testing.T) {
	noRules := func(string) bool { return false }

	// Baseline: with empty prev, every current port is new (and unfiltered).
	base := newPorts(nil, map[string]RemoteListener{
		"3000": {Port: "3000", Process: "node"},
		"6379": {Port: "6379", Process: "redis-server"},
	}, noRules)
	if ports := portsOf(base); !reflect.DeepEqual(ports, []string{"3000", "6379"}) {
		t.Fatalf("baseline new ports = %v, want [3000 6379]", ports)
	}

	// Only ports absent from prev are suggested.
	prev := map[string]RemoteListener{"3000": {Port: "3000"}}
	cur := map[string]RemoteListener{
		"3000": {Port: "3000"},
		"5173": {Port: "5173", Process: "vite"},
	}
	got := newPorts(prev, cur, noRules)
	want := []Suggestion{{Port: "5173", Process: "vite"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newPorts = %+v, want %+v", got, want)
	}

	// A port already forwarded by a rule is filtered out.
	hasRule := func(port string) bool { return port == "5173" }
	if got := newPorts(prev, cur, hasRule); len(got) != 0 {
		t.Fatalf("newPorts with rule = %+v, want empty", got)
	}
}

// portsOf returns the sorted ports of a suggestion slice, since newPorts iterates
// a map and does not guarantee order.
func portsOf(ss []Suggestion) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Port
	}
	sort.Strings(out)
	return out
}
