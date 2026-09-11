//go:build unix

package index

import "syscall"

func lockFD(fd int, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	return syscall.Flock(fd, how)
}

func unlockFD(fd int) error { return syscall.Flock(fd, syscall.LOCK_UN) }
