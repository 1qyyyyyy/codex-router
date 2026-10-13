//go:build !windows

package main

import (
	"fmt"
	"io"
)

func backgroundCommand(action, path string, out io.Writer) error {
	return fmt.Errorf("后台代理管理仅支持视窗系统")
}

func diagnoseRouting(path string, out io.Writer) error { return nil }
