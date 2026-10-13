//go:build windows

package integration

import (
	"fmt"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

type connectionOption struct {
	Option uint32
	Value  uintptr
}

type connectionOptions struct {
	Size       uint32
	Connection *uint16
	Count      uint32
	Error      uint32
	Options    *connectionOption
}

func queryConnectionFlags() (uint32, error) {
	option := connectionOption{Option: 1}
	list := connectionOptions{Count: 1, Options: &option}
	list.Size = uint32(unsafe.Sizeof(list))
	size := list.Size
	result, _, err := wininet.NewProc("InternetQueryOptionW").Call(0, 75, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	if result == 0 {
		return 0, fmt.Errorf("读取系统连接代理标志失败：%w", err)
	}
	return uint32(option.Value), nil
}

func setConnectionProxy(flags uint32, server, bypass, automatic string) error {
	serverText, err := windows.UTF16PtrFromString(server)
	if err != nil {
		return err
	}
	bypassText, err := windows.UTF16PtrFromString(bypass)
	if err != nil {
		return err
	}
	automaticText, err := windows.UTF16PtrFromString(automatic)
	if err != nil {
		return err
	}
	options := []connectionOption{
		{Option: 1, Value: uintptr(flags)},
		{Option: 2, Value: uintptr(unsafe.Pointer(serverText))},
		{Option: 3, Value: uintptr(unsafe.Pointer(bypassText))},
		{Option: 4, Value: uintptr(unsafe.Pointer(automaticText))},
	}
	list := connectionOptions{Count: uint32(len(options)), Options: &options[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	result, _, callErr := procInternetSetOption.Call(0, 75, uintptr(unsafe.Pointer(&list)), uintptr(list.Size))
	runtime.KeepAlive(serverText)
	runtime.KeepAlive(bypassText)
	runtime.KeepAlive(automaticText)
	if result == 0 {
		return fmt.Errorf("设置系统连接代理失败：%w", callErr)
	}
	return nil
}
