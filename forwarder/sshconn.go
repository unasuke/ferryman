package forwarder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Profile is a saved connection: an ssh_config alias (or hostname) plus
// optional overrides and the forward rules.
type Profile struct {
	Host          string   `json:"host"` // ssh_config alias or hostname
	User          string   `json:"user,omitempty"`
	Port          string   `json:"port,omitempty"`
	IdentityFiles []string `json:"identity_files,omitempty"`
	KnownHosts    string   `json:"known_hosts,omitempty"`
	Rules         []Rule   `json:"rules"`
}

// PassphraseFunc unlocks an encrypted private key.
type PassphraseFunc func(keyPath string) (string, error)

// TrustFunc decides whether to trust an unknown host key (TOFU).
type TrustFunc func(host string, key ssh.PublicKey) bool

// NewDialer builds a Dialer from a Profile. It reuses ~/.ssh/config, the
// ssh-agent (OpenSSH agent on Windows, SSH_AUTH_SOCK elsewhere) and key files.
func NewDialer(p Profile, passphrase PassphraseFunc, trust TrustFunc) Dialer {
	return func(ctx context.Context) (*ssh.Client, error) {
		host, port := resolveHostPort(p)
		addr := net.JoinHostPort(host, port)

		auth, agentConn := authMethods(resolveIdentities(p), passphrase)
		// The agent connection is only needed for the handshake; close it once
		// this dial returns (on success and every error path) to avoid leaking
		// one socket per reconnect.
		defer agentConn.Close()
		if len(auth) == 0 {
			return nil, errors.New("no usable authentication methods (no agent, no keys)")
		}
		knownHostsPath := resolveKnownHosts(p)
		known, err := loadKnownHosts(knownHostsPath)
		if err != nil {
			return nil, err
		}
		cfg := &ssh.ClientConfig{
			User:            resolveUser(p),
			Auth:            auth,
			HostKeyCallback: hostKeyCallback(knownHostsPath, known, trust),
			// Offer only the host key types already pinned for this host, so the
			// handshake negotiates a key we can actually verify. Without this the
			// library's default order may select the server's RSA/ECDSA key while
			// known_hosts only holds its ed25519 key, which looks like a key
			// mismatch. A nil result (unknown host) keeps the default so TOFU can
			// pin whatever the server offers. This mirrors OpenSSH.
			HostKeyAlgorithms: knownHostKeyAlgorithms(known, addr),
			Timeout:           15 * time.Second,
		}
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
		if err != nil {
			conn.Close()
			return nil, err
		}
		return ssh.NewClient(c, chans, reqs), nil
	}
}

func resolveHostPort(p Profile) (host, port string) {
	host = p.Host
	if hn := ssh_config.Get(p.Host, "HostName"); hn != "" {
		host = hn
	}
	if port = p.Port; port == "" {
		port = ssh_config.Get(p.Host, "Port")
	}
	if port == "" {
		port = "22"
	}
	return host, port
}

func resolveUser(p Profile) string {
	if p.User != "" {
		return p.User
	}
	if u := ssh_config.Get(p.Host, "User"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func resolveIdentities(p Profile) []string {
	if len(p.IdentityFiles) > 0 {
		return p.IdentityFiles
	}
	var out []string
	for _, id := range ssh_config.GetAll(p.Host, "IdentityFile") {
		if id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		home, _ := os.UserHomeDir()
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			out = append(out, filepath.Join(home, ".ssh", name))
		}
	}
	return out
}

func resolveKnownHosts(p Profile) string {
	if p.KnownHosts != "" {
		return expand(p.KnownHosts)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "known_hosts")
}

// noopCloser is returned when no agent connection was opened, so callers can
// unconditionally Close the returned io.Closer.
type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// authMethods builds the auth method list and returns a closer for the agent
// connection (a no-op when no agent is available). The caller must Close it
// after the handshake completes.
func authMethods(identityFiles []string, passphrase PassphraseFunc) ([]ssh.AuthMethod, io.Closer) {
	var methods []ssh.AuthMethod
	var agentCloser io.Closer = noopCloser{}
	if conn, err := dialAgent(); err == nil {
		methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		agentCloser = conn
	}
	for _, path := range identityFiles {
		if s := loadSigner(expand(path), passphrase); s != nil {
			methods = append(methods, ssh.PublicKeys(s))
		}
	}
	return methods, agentCloser
}

func loadSigner(path string, passphrase PassphraseFunc) ssh.Signer {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // missing key file: just skip it
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return signer
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) && passphrase != nil {
		pass, perr := passphrase(path)
		if perr != nil {
			return nil
		}
		if s, err := ssh.ParsePrivateKeyWithPassphrase(data, []byte(pass)); err == nil {
			return s
		}
	}
	return nil
}

// loadKnownHosts returns a knownhosts callback for the file, or nil if the file
// does not exist yet (first-time use).
func loadKnownHosts(path string) (ssh.HostKeyCallback, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return knownhosts.New(path)
}

// hostKeyCallback wraps the known_hosts check with TOFU: a matching key passes,
// a changed key is refused (possible MITM), and an unknown host is pinned after
// trust confirmation.
func hostKeyCallback(knownHostsPath string, known ssh.HostKeyCallback, trust TrustFunc) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if known != nil {
			err := known(hostname, remote, key)
			if err == nil {
				return nil
			}
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) && len(ke.Want) > 0 {
				// A different key is already on file: refuse (possible MITM).
				return fmt.Errorf("host key mismatch for %s: %w", hostname, err)
			}
			// Otherwise the host is simply unknown: fall through to TOFU.
		}
		if trust == nil || !trust(hostname, key) {
			return fmt.Errorf("host key for %s not trusted", hostname)
		}
		return appendKnownHost(knownHostsPath, hostname, key)
	}
}

// knownHostKeyAlgorithms returns the host key algorithms already pinned for addr
// in known_hosts, most-preferred first, or nil if the host is unknown. Setting
// these on ClientConfig makes the handshake negotiate a key type we can verify,
// matching what OpenSSH does with its known_hosts-driven preference.
func knownHostKeyAlgorithms(known ssh.HostKeyCallback, addr string) []string {
	if known == nil {
		return nil
	}
	// Probe with a throwaway key: for a known host the callback returns a
	// KeyError whose Want lists the pinned keys, whose types we then offer.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	probe, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return nil
	}
	var ke *knownhosts.KeyError
	if !errors.As(known(addr, staticAddr(addr), probe), &ke) || len(ke.Want) == 0 {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	for _, w := range ke.Want {
		for _, a := range hostKeyAlgorithmsFor(w.Key.Type()) {
			if !seen[a] {
				seen[a] = true
				algos = append(algos, a)
			}
		}
	}
	return algos
}

// hostKeyAlgorithmsFor expands a stored key type into the signature algorithms
// to offer for it. An RSA host key can be verified with the modern SHA-2
// signatures, which many servers require in place of legacy ssh-rsa (SHA-1).
func hostKeyAlgorithmsFor(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// staticAddr is a net.Addr backed by a fixed host:port string, used to probe
// the known_hosts callback without opening a connection.
type staticAddr string

func (staticAddr) Network() string  { return "tcp" }
func (a staticAddr) String() string { return string(a) }

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	_, err = f.WriteString(line + "\n")
	return err
}

func expand(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
