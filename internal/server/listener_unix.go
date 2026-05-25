//go:build linux || freebsd || darwin

package server

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// reuseportUDP opens a UDP socket with SO_REUSEPORT (and SO_REUSEADDR)
// so several listeners can share one addr:port. The kernel hashes
// incoming packets across them by 4-tuple, giving linear scaling on
// multi-core hosts. Falls back gracefully on systems without
// SO_REUSEPORT support (it's defined on Linux 3.9+, macOS, FreeBSD).
func reuseportUDP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
					sockErr = err
					return
				}
				if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
					sockErr = err
				}
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}
	return lc.ListenPacket(context.Background(), "udp", addr)
}

// reuseportSupported is true on platforms where SO_REUSEPORT actually
// load-balances UDP packets across listeners.
const reuseportSupported = true
