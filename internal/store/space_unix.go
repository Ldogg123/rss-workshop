//go:build unix

package store

import "golang.org/x/sys/unix"

// freeSpace reports the bytes an unprivileged process can still write in dir.
func freeSpace(dir string) (uint64, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, false
	}
	return stat.Bavail * uint64(stat.Bsize), true
}
