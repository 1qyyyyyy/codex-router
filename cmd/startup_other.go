//go:build !windows

package main

func runDefault() error {
	return runService("")
}

func showStartupError(error) bool {
	return false
}
