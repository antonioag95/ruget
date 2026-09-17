//go:build !windows

package main

// enableVirtualTerminal is a no-op on platforms where ANSI escapes are native.
func enableVirtualTerminal() {}
