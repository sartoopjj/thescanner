//go:build unix

package main

import (
	"log"
	"syscall"
)

// raiseFDLimit bumps RLIMIT_NOFILE to the kernel's hard limit. The DNS
// path tends to hold many concurrent sockets (per-query UDP/TCP + stats
// HTTP), and most distros ship with a soft cap (often 1024) that bites
// well before the hard cap (often 1048576+). systemd takes care of this
// in the packaged unit; this is the safety net for everything else
// (FreeBSD, manual launches, docker without an explicit ulimit, etc).
func raiseFDLimit() {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		log.Printf("rlimit: getrlimit NOFILE failed: %v", err)
		return
	}
	prev := rl.Cur
	if rl.Cur < rl.Max {
		rl.Cur = rl.Max
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
			log.Printf("rlimit: setrlimit NOFILE → %d failed: %v", rl.Max, err)
			return
		}
		log.Printf("rlimit: raised NOFILE %d → %d", prev, rl.Max)
	} else {
		log.Printf("rlimit: NOFILE already at hard cap %d", rl.Cur)
	}
}
