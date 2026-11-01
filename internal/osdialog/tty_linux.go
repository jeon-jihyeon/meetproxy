//go:build linux

package osdialog

import "syscall"

const ioctlGetTermios = syscall.TCGETS
