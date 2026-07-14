package forwarder

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func genPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func writeKnownHost(t *testing.T, path, hostAddr string, key ssh.PublicKey) {
	t.Helper()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostAddr)}, key)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got, want := expand("~/foo"), filepath.Join(home, "foo"); got != want {
		t.Errorf("expand(~/foo) = %q, want %q", got, want)
	}
	for _, p := range []string{"/abs/path", "relative", "~notme"} {
		if got := expand(p); got != p {
			t.Errorf("expand(%q) = %q, want unchanged", p, got)
		}
	}
}

func TestHostKeyAlgorithmsFor(t *testing.T) {
	if got := hostKeyAlgorithmsFor(ssh.KeyAlgoRSA); !slices.Equal(got, []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA}) {
		t.Errorf("rsa expansion = %v", got)
	}
	if got := hostKeyAlgorithmsFor(ssh.KeyAlgoED25519); !slices.Equal(got, []string{ssh.KeyAlgoED25519}) {
		t.Errorf("ed25519 = %v", got)
	}
	if got := hostKeyAlgorithmsFor(ssh.KeyAlgoECDSA256); !slices.Equal(got, []string{ssh.KeyAlgoECDSA256}) {
		t.Errorf("ecdsa = %v", got)
	}
}

func TestKnownHostKeyAlgorithms(t *testing.T) {
	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")
	writeKnownHost(t, khPath, "127.0.0.1:22", genPublicKey(t))

	known, err := loadKnownHosts(khPath)
	if err != nil {
		t.Fatal(err)
	}
	if known == nil {
		t.Fatal("loadKnownHosts returned nil for an existing file")
	}
	if got := knownHostKeyAlgorithms(known, "127.0.0.1:22"); !slices.Equal(got, []string{ssh.KeyAlgoED25519}) {
		t.Errorf("known host algos = %v, want [ssh-ed25519]", got)
	}
	if got := knownHostKeyAlgorithms(known, "10.0.0.99:22"); got != nil {
		t.Errorf("unknown host algos = %v, want nil", got)
	}

	// Missing file: loadKnownHosts is nil, so algorithms are nil too.
	missing, err := loadKnownHosts(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Error("loadKnownHosts should be nil for a missing file")
	}
	if got := knownHostKeyAlgorithms(missing, "127.0.0.1:22"); got != nil {
		t.Errorf("nil-known algos = %v, want nil", got)
	}
}

func TestHostKeyCallbackTOFU(t *testing.T) {
	const hostAddr = "127.0.0.1:22"
	remote := staticAddr(hostAddr)
	keyA := genPublicKey(t)
	keyB := genPublicKey(t)

	t.Run("match", func(t *testing.T) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHost(t, kh, hostAddr, keyA)
		known, _ := loadKnownHosts(kh)
		cb := hostKeyCallback(kh, known, func(string, ssh.PublicKey) bool {
			t.Fatal("trust must not be called on a match")
			return false
		})
		if err := cb(hostAddr, remote, keyA); err != nil {
			t.Errorf("matching key should pass: %v", err)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		writeKnownHost(t, kh, hostAddr, keyA)
		known, _ := loadKnownHosts(kh)
		trustCalled := false
		cb := hostKeyCallback(kh, known, func(string, ssh.PublicKey) bool {
			trustCalled = true
			return true
		})
		if err := cb(hostAddr, remote, keyB); err == nil {
			t.Error("a changed key must be refused")
		}
		if trustCalled {
			t.Error("trust must not be consulted on a key mismatch")
		}
	})

	t.Run("unknown-trusted", func(t *testing.T) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		known, _ := loadKnownHosts(kh) // nil: file absent
		cb := hostKeyCallback(kh, known, func(string, ssh.PublicKey) bool { return true })
		if err := cb(hostAddr, remote, keyA); err != nil {
			t.Fatalf("unknown host with trust should pass: %v", err)
		}
		// The key is now pinned; a re-verify passes without consulting trust.
		known2, _ := loadKnownHosts(kh)
		if known2 == nil {
			t.Fatal("known_hosts was not written")
		}
		cb2 := hostKeyCallback(kh, known2, func(string, ssh.PublicKey) bool {
			t.Fatal("trust must not be called; host is now known")
			return false
		})
		if err := cb2(hostAddr, remote, keyA); err != nil {
			t.Errorf("re-verify after pinning failed: %v", err)
		}
	})

	t.Run("unknown-untrusted", func(t *testing.T) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		known, _ := loadKnownHosts(kh)
		if err := hostKeyCallback(kh, known, func(string, ssh.PublicKey) bool { return false })(hostAddr, remote, keyA); err == nil {
			t.Error("unknown host with trust=false must be refused")
		}
		if err := hostKeyCallback(kh, known, nil)(hostAddr, remote, keyA); err == nil {
			t.Error("unknown host with trust=nil must be refused")
		}
	})
}

func TestResolveOverrides(t *testing.T) {
	// Explicit overrides are deterministic and bypass ssh_config global state.
	p := Profile{
		Host:          "example-does-not-exist-xyz",
		Port:          "2222",
		User:          "alice",
		IdentityFiles: []string{"/keys/id_test"},
	}
	if _, port := resolveHostPort(p); port != "2222" {
		t.Errorf("port override = %q, want 2222", port)
	}
	if u := resolveUser(p); u != "alice" {
		t.Errorf("user override = %q, want alice", u)
	}
	if ids := resolveIdentities(p); !slices.Equal(ids, []string{"/keys/id_test"}) {
		t.Errorf("identity override = %v", ids)
	}
}
