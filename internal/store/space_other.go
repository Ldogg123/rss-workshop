//go:build !unix

package store

// freeSpace is unknown here, so the copy proceeds and reports any write error.
func freeSpace(string) (uint64, bool) { return 0, false }
