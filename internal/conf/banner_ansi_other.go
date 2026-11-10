//go:build !windows

package conf

var ansiSupported = func(uintptr) bool { return true }
