//go:build !windows

package main

import "os/exec"

func newCmd(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

func focusMainWindow() bool { return false }

func closeShellWindows() {}

func fatalDialog(string) {}
