//go:build !windows

package forwarder

import (
	"errors"
	"net"
	"os"
)

// dialAgent connects to the SSH agent via the SSH_AUTH_SOCK unix socket.
func dialAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK not set")
	}
	return net.Dial("unix", sock)
}
