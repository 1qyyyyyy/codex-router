//go:build windows

package integration

import (
	"syscall"
)

var wininet = syscall.NewLazyDLL("wininet.dll")
var procInternetSetOption = wininet.NewProc("InternetSetOptionW")

func callInternetSetOption(option uintptr) error {
	result, _, err := procInternetSetOption.Call(0, option, 0, 0)
	if result == 0 {
		if errno, ok := err.(syscall.Errno); ok && errno != 0 {
			return errno
		}
		return syscall.EINVAL
	}
	return nil
}
