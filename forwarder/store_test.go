package forwarder

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	// A nested path also exercises directory creation.
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	p := Profile{
		Host: "myserver",
		User: "bob",
		Rules: []Rule{
			{Name: "web", LocalAddr: "127.0.0.1:8080", RemoteAddr: "localhost:3000", Enabled: true},
		},
	}
	if err := SaveProfile(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, p)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"host"`, `"local_addr"`, `"remote_addr"`, `"enabled"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("serialized config missing JSON key %s", key)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	got, err := LoadProfile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got.Host != "" || len(got.Rules) != 0 {
		t.Errorf("missing file should yield an empty profile, got %+v", got)
	}
}
