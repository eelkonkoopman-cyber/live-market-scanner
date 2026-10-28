package main

import "testing"

// TestNewCmdDoesNotHideGUIShell guards the 2026-10-02 regression where every
// scanner window opened invisible. HideWindow puts SW_HIDE in the child's
// STARTUPINFO, and Windows applies that to the shell's FIRST ShowWindow call,
// silently turning go-webview2's ShowWindow(SW_SHOW) into a hide.
func TestNewCmdDoesNotHideGUIShell(t *testing.T) {
	cmd := newCmd("scannerwin.exe", "--pane", "main")
	if cmd.SysProcAttr == nil {
		t.Fatal("newCmd must set SysProcAttr (CREATE_NO_WINDOW)")
	}
	if cmd.SysProcAttr.HideWindow {
		t.Fatal("newCmd must not set HideWindow: it makes the scannerwin window open invisible")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatal("newCmd should keep CREATE_NO_WINDOW so a console-subsystem dev build never pops a console")
	}
}
