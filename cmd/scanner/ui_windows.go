//go:build windows

package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procFindWindowW         = user32.NewProc("FindWindowW")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procGetConsoleWindow    = kernel32.NewProc("GetConsoleWindow")
)

const (
	swRestore = 9 // SW_RESTORE

	mbOK            = 0x0
	mbIconError     = 0x10
	mbSetForeground = 0x10000

	createNoWindow = 0x08000000

	mainWindowTitle = "Market Scanner" // scannerwin's main-pane window title
)

// newCmd builds the command that launches the scannerwin.exe GUI shell. It sets
// CREATE_NO_WINDOW (a no-op for the GUI-subsystem release build, and it keeps a
// console-subsystem dev build from popping a console) but must NOT set
// HideWindow: that puts SW_HIDE in the child's STARTUPINFO, and Windows applies
// STARTUPINFO's show state to a process's FIRST ShowWindow call — so
// go-webview2's ShowWindow(SW_SHOW) silently became a hide and every window
// opened invisible (2026-10-02). taskkill in closeShellWindows is a console
// tool, so HideWindow stays correct there.
func newCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd
}

// focusMainWindow restores and raises the native main window if it exists.
func focusMainWindow() bool {
	title, err := windows.UTF16PtrFromString(mainWindowTitle)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procSetForegroundWindow.Call(hwnd)
	return true
}

// closeShellWindows asks every scannerwin.exe to close (WM_CLOSE, no /F) so the
// windows go away with the scanner. Best effort, bounded to 3 s.
func closeShellWindows() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "taskkill", "/IM", shellExeName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = cmd.Run()
}

// fatalDialog shows msg in a message box when there is no console to print to.
func fatalDialog(msg string) {
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		return
	}
	text, err := windows.UTF16PtrFromString(msg)
	if err != nil {
		return
	}
	caption, _ := windows.UTF16PtrFromString("Live Market Scanner")
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(caption)), mbOK|mbIconError|mbSetForeground)
}
