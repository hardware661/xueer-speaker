//go:build !windows

package main

import (
	"os"
	"syscall"
)

// private: owned by us and not readable by group or others.
func private(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return !ok || (int(sys.Uid) == os.Getuid() && st.Mode().Perm()&0o077 == 0)
}
