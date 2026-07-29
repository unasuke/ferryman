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

// snapshotOf builds a snapshot from bare ports, so a case can be written as a
// port list. Process names are irrelevant to the streak logic and left empty.
func snapshotOf(ports ...string) map[string]RemoteListener {
	out := make(map[string]RemoteListener, len(ports))
	for _, p := range ports {
		out[p] = RemoteListener{Port: p}
	}
	return out
}

func TestBumpStreaks(t *testing.T) {
	// need is passed literally so the cases do not depend on requiredScans.
	const need = 2

	tests := []struct {
		name      string
		streak    map[string]int
		prev, cur []string
		want      map[string]int
	}{
		{"baseline stays uncounted", nil, []string{"3000"}, []string{"3000"}, map[string]int{}},
		{"new port starts a streak", nil, nil, []string{"3000"}, map[string]int{"3000": 1}},
		{"candidate keeps counting", map[string]int{"3000": 1}, []string{"3000"}, []string{"3000"}, map[string]int{"3000": 2}},
		{"ripe port clamps", map[string]int{"3000": 2}, []string{"3000"}, []string{"3000"}, map[string]int{"3000": 3}},
		{"suggested port stays clamped", map[string]int{"3000": 3}, []string{"3000"}, []string{"3000"}, map[string]int{"3000": 3}},
		{"vanished port is dropped", map[string]int{"9222": 1}, []string{"9222"}, nil, map[string]int{}},
		{"reopened port restarts", nil, nil, []string{"9222"}, map[string]int{"9222": 1}},
		{
			"mixed baseline and candidate",
			map[string]int{"3000": 1},
			[]string{"3000", "6379"},
			[]string{"3000", "6379"},
			map[string]int{"3000": 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bumpStreaks(tc.streak, snapshotOf(tc.prev...), snapshotOf(tc.cur...), need)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("bumpStreaks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfirmedPorts(t *testing.T) {
	const need = 2

	tests := []struct {
		name   string
		streak map[string]int
		cur    []string
		ruled  []string
		want   []string
	}{
		{"below the threshold", map[string]int{"3000": 1}, []string{"3000"}, nil, []string{}},
		{"just reached the threshold", map[string]int{"3000": 2}, []string{"3000"}, nil, []string{"3000"}},
		{"already suggested", map[string]int{"3000": 3}, []string{"3000"}, nil, []string{}},
		{"baseline port", nil, []string{"6379"}, nil, []string{}},
		{"already forwarded", map[string]int{"5173": 2}, []string{"5173"}, []string{"5173"}, []string{}},
		{
			"multiple ripe ports",
			map[string]int{"3000": 2, "5173": 2},
			[]string{"3000", "5173"},
			nil,
			[]string{"3000", "5173"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hasRule := func(port string) bool {
				for _, r := range tc.ruled {
					if r == port {
						return true
					}
				}
				return false
			}
			got := portsOf(confirmedPorts(tc.streak, snapshotOf(tc.cur...), need, hasRule))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("confirmedPorts = %v, want %v", got, tc.want)
			}
		})
	}

	// The process name is carried over from the confirming scan.
	cur := map[string]RemoteListener{"3000": {Port: "3000", Process: "node"}}
	got := confirmedPorts(map[string]int{"3000": need}, cur, need, func(string) bool { return false })
	want := []Suggestion{{Port: "3000", Process: "node"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("confirmedPorts = %+v, want %+v", got, want)
	}
}

// portsOf returns the sorted ports of a suggestion slice, since confirmedPorts
// iterates a map and does not guarantee order.
func portsOf(ss []Suggestion) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Port
	}
	sort.Strings(out)
	return out
}
