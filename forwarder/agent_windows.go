//go:build windows

package forwarder

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// dialAgent connects to the Windows OpenSSH agent over its named pipe.
// (Start it with: Start-Service ssh-agent; ssh-add.)
func dialAgent() (net.Conn, error) {
	return winio.DialPipe(`\\.\pipe\openssh-ssh-agent`, nil)
}
