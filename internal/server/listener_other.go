//go:build !linux && !freebsd && !darwin

package server

import "net"

// reuseportUDP without SO_REUSEPORT — one listener, one goroutine
// reading packets. Windows and other platforms fall through here.
func reuseportUDP(addr string) (net.PacketConn, error) {
	return net.ListenPacket("udp", addr)
}

const reuseportSupported = false
