package daemon

import (
	"os"
	"runtime"
)

// openFDCount returns the number of file descriptors the current process has
// open, or -1 when the platform does not expose this. Only Linux is supported
// (via /proc/self/fd) — that is the production target (EKS/Fargate). On other
// platforms (e.g. a developer's Windows/macOS box) it returns -1, which makes
// the periodic log field read "unknown" and the fd watchdog inert.
//
// The count is approximate: os.ReadDir itself holds the directory fd open while
// listing, so we subtract one to exclude it.
func openFDCount() int {
	if runtime.GOOS != "linux" {
		return -1
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	if n := len(entries) - 1; n >= 0 {
		return n
	}
	return 0
}
