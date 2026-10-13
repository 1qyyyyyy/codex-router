//go:build windows

package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestDesktopNativeStructureLayout(t *testing.T) {
	if syscall.NewCallback(desktopWindowProc) == 0 {
		t.Fatal("无法创建原生窗口回调")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("此校验针对六十四位原生结构")
	}
	for _, item := range []struct {
		name             string
		actual, expected uintptr
	}{
		{"窗口类", unsafe.Sizeof(desktopClass{}), 80},
		{"窗口消息", unsafe.Sizeof(desktopMessage{}), 48},
		{"托盘通知", unsafe.Sizeof(desktopNotify{}), 976},
		{"托盘窗口偏移", unsafe.Offsetof(desktopNotify{}.Window), 8},
		{"消息参数偏移", unsafe.Offsetof(desktopMessage{}.WParam), 16},
	} {
		if item.actual != item.expected {
			t.Errorf("%s结构不匹配：实际%d，预期%d", item.name, item.actual, item.expected)
		}
	}
}

func TestDesktopButtonBackgroundColors(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dc, _, _ := desktopGDI.NewProc("CreateCompatibleDC").Call(0)
	if dc == 0 {
		t.Fatal("无法创建绘图验证环境")
	}
	defer desktopGDI.NewProc("DeleteDC").Call(dc)
	bitmap, _, _ := desktopGDI.NewProc("CreateBitmap").Call(142, 42, 1, 32, 0)
	if bitmap == 0 {
		t.Fatal("无法创建验证位图")
	}
	defer desktopGDI.NewProc("DeleteObject").Call(bitmap)
	previous, _, _ := desktopGDI.NewProc("SelectObject").Call(dc, bitmap)
	defer desktopGDI.NewProc("SelectObject").Call(dc, previous)
	font, _, _ := desktopGDI.NewProc("GetStockObject").Call(17)
	view := desktopView{bodyFont: font}
	for _, item := range []struct {
		name      string
		id, state uint32
		color     uintptr
	}{
		{"启动", 101, 0, rgb(52, 174, 122)},
		{"停止", 102, 0, rgb(185, 82, 93)},
		{"检测", 103, 0, rgb(62, 132, 205)},
		{"设置", 104, 0, rgb(43, 59, 80)},
		{"按下", 101, 1, rgb(35, 48, 66)},
		{"禁用", 101, 4, rgb(34, 44, 58)},
	} {
		view.drawButton(&desktopDrawItem{CtlID: item.id, ItemState: item.state, hDC: dc, rcItem: desktopRect{0, 0, 142, 42}})
		pixel, _, _ := desktopGDI.NewProc("GetPixel").Call(dc, 8, 8)
		if pixel != item.color {
			t.Errorf("%s按钮底色不符：实际%x，预期%x", item.name, pixel, item.color)
		}
	}
}
