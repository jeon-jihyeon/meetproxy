//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package osdialog

import "syscall"

const ioctlGetTermios = syscall.TIOCGETA
