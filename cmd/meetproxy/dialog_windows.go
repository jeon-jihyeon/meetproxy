//go:build windows

package main

import "os/exec"

// Windows shows no meetproxy dialog, so these are never reached
func newGroup(*exec.Cmd) {}

func ownGroup(*exec.Cmd) {}

func killGroup(shownDialog, string) {}

func startedAt(int) string { return "" }
