package main

import (
	"testing"

	"github.com/unasuke/ferryman/forwarder"
)

func TestWithDefaultHost(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		defaultHost string
		want        string
	}{
		{"bare local port", "8080", "127.0.0.1", "127.0.0.1:8080"},
		{"bare remote port", "3000", "localhost", "localhost:3000"},
		{"full ipv4", "127.0.0.1:8080", "127.0.0.1", "127.0.0.1:8080"},
		{"ipv6", "[::1]:5173", "localhost", "[::1]:5173"},
		{"empty host", ":8080", "127.0.0.1", ":8080"},
		{"all interfaces", "0.0.0.0:8080", "127.0.0.1", "0.0.0.0:8080"},
		{"trimmed bare port", "  8080  ", "127.0.0.1", "127.0.0.1:8080"},
		{"empty", "", "127.0.0.1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withDefaultHost(c.input, c.defaultHost); got != c.want {
				t.Errorf("withDefaultHost(%q, %q) = %q, want %q", c.input, c.defaultHost, got, c.want)
			}
		})
	}
}

func TestSuggestedRule(t *testing.T) {
	allFree := func(string) bool { return true }
	// unavailable returns a predicate that treats the given local addresses as
	// used/unbindable and every other address as free.
	unavailable := func(addrs ...string) func(string) bool {
		busy := map[string]bool{}
		for _, a := range addrs {
			busy[a] = true
		}
		return func(addr string) bool { return !busy[addr] }
	}

	// Default: remote keeps the port, local mirrors it, Name is the process.
	got := suggestedRule(forwarder.Suggestion{Port: "3000", Process: "node"}, allFree)
	want := forwarder.Rule{Name: "node", LocalAddr: "127.0.0.1:3000", RemoteAddr: "localhost:3000", Enabled: true}
	if got != want {
		t.Errorf("suggestedRule default = %+v, want %+v", got, want)
	}

	// A taken/unbindable local address bumps the local port to the next free one.
	got = suggestedRule(forwarder.Suggestion{Port: "3000", Process: "node"}, unavailable("127.0.0.1:3000"))
	if got.LocalAddr != "127.0.0.1:3001" {
		t.Errorf("suggestedRule local with collision = %q, want 127.0.0.1:3001", got.LocalAddr)
	}
	if got.RemoteAddr != "localhost:3000" {
		t.Errorf("suggestedRule remote = %q, want localhost:3000", got.RemoteAddr)
	}

	// Multiple consecutive collisions keep bumping until a free port is found.
	if got := suggestedRule(forwarder.Suggestion{Port: "3000"}, unavailable("127.0.0.1:3000", "127.0.0.1:3001")); got.LocalAddr != "127.0.0.1:3002" {
		t.Errorf("suggestedRule local with two collisions = %q, want 127.0.0.1:3002", got.LocalAddr)
	}

	// An empty process name yields an empty rule name.
	if got := suggestedRule(forwarder.Suggestion{Port: "5173"}, allFree); got.Name != "" {
		t.Errorf("suggestedRule Name with no process = %q, want empty", got.Name)
	}
}
