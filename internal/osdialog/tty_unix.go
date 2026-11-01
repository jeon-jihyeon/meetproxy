//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package osdialog

import (
	"syscall"
	"unsafe"
)

func fdIsTerminal(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlGetTermios, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
