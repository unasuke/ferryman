package main

import "testing"

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
