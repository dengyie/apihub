//go:build windows

package main

import (
	"net"

	"github.com/dengyie/apihub/common"
)

// listenWithOptions binds addr and returns a listening socket.
//
// Windows has no SO_REUSEPORT, so the zero-downtime pair is unavailable here and
// reusePort is reported rather than silently ignored: a deploy script pairing
// two processes on one port must not run against a binary that quietly
// refuses to share it.
func listenWithOptions(addr string, reusePort bool) (net.Listener, error) {
	if reusePort {
		common.SysLog("APIHUB_REUSEPORT requested but unsupported on this platform; " +
			"falling back to a single-process bind on " + addr)
	}
	return net.Listen("tcp", addr)
}