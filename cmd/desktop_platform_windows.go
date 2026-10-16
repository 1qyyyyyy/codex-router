//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var desktopDWM = windows.NewLazySystemDLL("dwmapi.dll")
var desktopShcore = windows.NewLazySystemDLL("shcore.dll")
var desktopFontCallback = syscall.NewCallback(desktopFontChildProc)

const desktopBaseDPI uint32 = 96

func desktopEnableDPI() {
	setContext := desktopUser.NewProc("SetProcessDpiAwarenessContext")
	if setContext.Find() == nil {
		// -4 是 Windows 公布的每显示器 v2 DPI 感知上下文。
		if result, _, _ := setContext.Call(^uintptr(3)); result != 0 {
			return
		}
	}
	setAwareness := desktopShcore.NewProc("SetProcessDpiAwareness")
	if setAwareness.Find() == nil {
		// 2 表示每显示器 DPI 感知，适用于不支持 v2 的系统。
		if result, _, _ := setAwareness.Call(2); int32(result) >= 0 {
			return
		}
	}
	setLegacy := desktopUser.NewProc("SetProcessDPIAware")
	if setLegacy.Find() == nil {
		setLegacy.Call()
	}
}

func desktopWindowDPI(window uintptr) uint32 {
	getDPI := desktopUser.NewProc("GetDpiForWindow")
	if getDPI.Find() == nil {
		if dpi, _, _ := getDPI.Call(window); dpi != 0 {
			return uint32(dpi)
		}
	}
	getSystemDPI := desktopUser.NewProc("GetDpiForSystem")
	if getSystemDPI.Find() == nil {
		if dpi, _, _ := getSystemDPI.Call(); dpi != 0 {
			return uint32(dpi)
		}
	}
	return desktopBaseDPI
}

func desktopScale(value int, dpi uint32) int {
	if dpi == 0 {
		dpi = desktopBaseDPI
	}
	product := value * int(dpi)
	if product >= 0 {
		return (product + int(desktopBaseDPI/2)) / int(desktopBaseDPI)
	}
	return (product - int(desktopBaseDPI/2)) / int(desktopBaseDPI)
}

func desktopFontChildProc(child, _ uintptr) uintptr {
	v := desktopCurrent
	if v == nil {
		return 1
	}
	font := v.bodyFont
	if child == v.output {
		font = v.font
	} else if child == v.settingsLabels[0] || child == v.settingsLabels[1] {
		font = v.labelFont
	}
	desktopCall("SendMessageW", child, 0x30, font, 1)
	if child == v.output || child == v.upstreamHost || child == v.upstreamPort {
		margin := desktopScale(10, v.dpi)
		packed := uintptr(uint16(margin)) | uintptr(uint16(margin))<<16
		desktopCall("SendMessageW", child, 0xD3, 3, packed)
	}
	return 1
}

func desktopApplyTitleTheme(window uintptr) {
	setAttribute := desktopDWM.NewProc("DwmSetWindowAttribute")
	if setAttribute.Find() != nil {
		return
	}
	dark := int32(1)
	// 20 是微软公开的 DWMWA_USE_IMMERSIVE_DARK_MODE；旧系统失败时保留系统标题栏主题。
	setAttribute.Call(window, 20, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
}

func (v *desktopView) rebuildFonts(dpi uint32) {
	if dpi == 0 {
		dpi = desktopBaseDPI
	}
	v.dpi = dpi
	createFont := desktopGDI.NewProc("CreateFontW")
	makeFont := func(points, weight int, face string) uintptr {
		height := -desktopScale(points, dpi)
		font, _, _ := createFont.Call(
			uintptr(height), 0, 0, 0, uintptr(weight), 0, 0, 0,
			1, 0, 0, 5, 0, uintptr(unsafe.Pointer(desktopString(face))),
		)
		return font
	}
	newLog := makeFont(13, 400, "Consolas")
	newTitle := makeFont(22, 600, "微软雅黑")
	newBody := makeFont(14, 400, "微软雅黑")
	newLabel := makeFont(12, 400, "微软雅黑")
	oldFonts := []uintptr{v.font, v.titleFont, v.bodyFont, v.labelFont}
	v.font, v.titleFont, v.bodyFont, v.labelFont = newLog, newTitle, newBody, newLabel
	if v.window != 0 {
		desktopCall("EnumChildWindows", v.window, desktopFontCallback, 0)
	}
	for _, font := range oldFonts {
		if font != 0 {
			desktopGDI.NewProc("DeleteObject").Call(font)
		}
	}
}

func desktopApplyDPIChange(v *desktopView, wparam, lparam uintptr) {
	if v == nil || lparam == 0 {
		return
	}
	dpi := uint32((wparam >> 16) & 0xffff)
	if dpi == 0 {
		dpi = desktopWindowDPI(v.window)
	}
	var suggested desktopRect
	windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory").Call(
		uintptr(unsafe.Pointer(&suggested)), lparam, unsafe.Sizeof(suggested),
	)
	v.dpi = dpi
	v.rebuildFonts(dpi)
	width := int(suggested.Right - suggested.Left)
	height := int(suggested.Bottom - suggested.Top)
	desktopCall("SetWindowPos", v.window, 0, uintptr(int(suggested.Left)), uintptr(int(suggested.Top)), uintptr(width), uintptr(height), 0x0004|0x0010)
	v.layout()
}

func desktopFitInitialWindow(v *desktopView) {
	if v == nil || v.window == 0 {
		return
	}
	dpi := v.dpi
	if dpi == 0 {
		dpi = desktopWindowDPI(v.window)
	}
	monitor := desktopCall("MonitorFromWindow", v.window, 2)
	info := struct {
		Size    uint32
		Monitor desktopRect
		Work    desktopRect
		Flags   uint32
	}{Size: uint32(unsafe.Sizeof(struct {
		Size    uint32
		Monitor desktopRect
		Work    desktopRect
		Flags   uint32
	}{}))}
	if monitor == 0 || desktopCall("GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))) == 0 {
		return
	}
	const style uintptr = 0x02CF0000
	var frame desktopRect
	frame.Right = int32(desktopScale(920, dpi))
	frame.Bottom = int32(desktopScale(660, dpi))
	adjust := desktopUser.NewProc("AdjustWindowRectExForDpi")
	if adjust.Find() == nil {
		adjust.Call(uintptr(unsafe.Pointer(&frame)), style, 0, 0, uintptr(dpi))
	} else {
		desktopCall("AdjustWindowRectEx", uintptr(unsafe.Pointer(&frame)), style, 0, 0)
	}
	frameWidth, frameHeight := frame.Right-frame.Left-int32(desktopScale(920, dpi)), frame.Bottom-frame.Top-int32(desktopScale(660, dpi))
	workWidth := info.Work.Right - info.Work.Left
	workHeight := info.Work.Bottom - info.Work.Top
	clientWidth := int32(desktopScale(920, dpi))
	clientHeight := int32(desktopScale(660, dpi))
	maxClientWidth := workWidth - frameWidth
	maxClientHeight := workHeight - frameHeight
	minClientWidth := int32(desktopScale(680, dpi))
	minClientHeight := int32(desktopScale(620, dpi))
	if clientWidth > maxClientWidth {
		clientWidth = maxClientWidth
		if clientWidth < minClientWidth {
			clientWidth = minClientWidth
		}
	}
	if clientHeight > maxClientHeight {
		clientHeight = maxClientHeight
		if clientHeight < minClientHeight {
			clientHeight = minClientHeight
		}
	}
	outerWidth := clientWidth + frameWidth
	outerHeight := clientHeight + frameHeight
	x := info.Work.Left + (workWidth-outerWidth)/2
	y := info.Work.Top + (workHeight-outerHeight)/2
	desktopCall("SetWindowPos", v.window, 0, uintptr(int(x)), uintptr(int(y)), uintptr(int(outerWidth)), uintptr(int(outerHeight)), 0x0004|0x0010)
}
