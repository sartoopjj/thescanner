package server

import (
	"os"
	"os/user"
	"strconv"
	"sync/atomic"
)

// sudoUID/sudoGID are resolved once at startup. When non-zero they
// override the effective uid/gid for files the server creates, so
// dev-time `sudo make run-server` doesn't leave root-owned files in
// the user's workspace.
var (
	sudoUID atomic.Int64
	sudoGID atomic.Int64
)

func init() {
	// Only meaningful when sudo is in play. SUDO_USER is set by sudo
	// to the invoking user's login name. If we're not euid 0 OR
	// SUDO_USER is empty, leave the atomics at 0 (= no-op).
	name := os.Getenv("SUDO_USER")
	if name == "" || os.Geteuid() != 0 {
		return
	}
	u, err := user.Lookup(name)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid > 0 {
		sudoUID.Store(int64(uid))
		sudoGID.Store(int64(gid))
	}
}

// DropRoot best-effort chowns `path` to the SUDO_USER's uid/gid when
// the server is running root-via-sudo. No-op on Windows (os.Chown
// returns ErrNotImplemented) and no-op when not running under sudo.
// Errors are intentionally swallowed — failure shouldn't crash the
// server, the file is still usable by root.
func DropRoot(path string) {
	uid := sudoUID.Load()
	if uid == 0 {
		return
	}
	_ = os.Chown(path, int(uid), int(sudoGID.Load()))
}

// dropRoot is the package-internal alias (older code uses lowercase).
func dropRoot(path string) { DropRoot(path) }
