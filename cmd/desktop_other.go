//go:build !windows

package main

import "fmt"

func runDesktop(path string) error {
	return fmt.Errorf("原生状态面板仅支持 Windows")
}
