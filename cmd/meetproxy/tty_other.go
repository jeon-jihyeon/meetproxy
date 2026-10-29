//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package main

// No terminal check here, so a passcode is never printed and goes to the dialog
func fdIsTerminal(uintptr) bool { return false }
