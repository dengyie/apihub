//go:build !windows

package main

import (
	"context"
	"net"
	"syscall"

	"github.com/dengyie/apihub/common"

	"golang.org/x/sys/unix"
)

// listenWithOptions binds addr and returns a listening socket.
//
// When reusePort is true the socket sets SO_REUSEPORT, which lets several
// processes bind the same address and splits incoming connections between them.
// That is what makes a restart invisible to clients: the incoming binary binds
// the port while the outgoing one is still serving, and only once the new
// process reports healthy does the deploy script drain the old one.
func listenWithOptions(addr string, reusePort bool) (net.Listener, error) {
	lc := net.ListenConfig{}
	if reusePort {
		lc.Control = func(network, address string, c syscall.RawConn) error {
			var setErr error
			// Report the setsockopt failure through the callback's own error
			// rather than swallowing it: binding without the flag would look
			// like a successful deploy while silently keeping the old
			// single-process behaviour.
			if err := c.Control(func(fd uintptr) {
				setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return setErr
		}
	}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	if reusePort {
		common.SysLog("SO_REUSEPORT enabled on " + addr + " (zero-downtime deploy mode)")
	}
	return ln, nil
}