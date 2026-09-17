//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVirtualTerminal turns on ANSI escape processing for the console so the
// CLI progress line renders correctly. It is a no-op when not attached to a
// console (e.g. output redirected).
func enableVirtualTerminal() {
	for _, handle := range []uintptr{os.Stdout.Fd(), os.Stderr.Fd()} {
		var mode uint32
		if err := windows.GetConsoleMode(windows.Handle(handle), &mode); err != nil {
			continue
		}
		_ = windows.SetConsoleMode(windows.Handle(handle), mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
