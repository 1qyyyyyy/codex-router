//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	desktopBackground    = rgb(23, 26, 32)
	desktopSurface       = rgb(32, 37, 45)
	desktopInput         = rgb(25, 30, 38)
	desktopBorder        = rgb(53, 61, 73)
	desktopTextPrimary   = rgb(237, 241, 247)
	desktopTextSecondary = rgb(170, 180, 194)
	desktopAccent        = rgb(76, 141, 255)
	desktopSuccess       = rgb(104, 210, 158)
	desktopWarning       = rgb(240, 194, 107)
	desktopFailure       = rgb(255, 145, 155)
)

type desktopLayout struct {
	status, settings, diagnosis                     desktopRect
	host, port, save, reload                        desktopRect
	output, copy, detail, main, diagnose, copyState desktopRect
	narrow                                          bool
}

// 所有几何量都从逻辑像素计算，绘制与原生子控件共用同一份布局。
func desktopMakeLayout(width, height int, dpi uint32) desktopLayout {
	if dpi == 0 {
		dpi = 96
	}
	w, h := width*96/int(dpi), height*96/int(dpi)
	logical := func(x, y, right, bottom int) desktopRect {
		return desktopRect{int32(desktopScale(x, dpi)), int32(desktopScale(y, dpi)), int32(desktopScale(right, dpi)), int32(desktopScale(bottom, dpi))}
	}
	narrow := w < 780
	settingsBottom := 404
	if narrow {
		settingsBottom = 416
	}
	diagTop := settingsBottom + 16
	portX := w - 44 - 120
	if !narrow {
		portX -= 142
	}
	l := desktopLayout{
		status: logical(24, 88, w-24, 244), settings: logical(24, 260, w-24, settingsBottom),
		diagnosis: logical(24, diagTop, w-24, h-34),
		host:      logical(44, 318, portX-16, 354), port: logical(portX, 318, portX+120, 354),
		save: logical(w-44-126, 318, w-44, 354), reload: logical(w-44-126, 272, w-44, 300),
		main: logical(w-44-132, 104, w-44, 140), diagnose: logical(w-24-108, 26, w-24, 62),
		copyState: logical(w-44-132-104, 106, w-44-132-12, 138),
		output:    logical(44, diagTop+76, w-44, h-48),
		copy:      logical(w-44-88, diagTop+14, w-44, diagTop+44),
		detail:    logical(w-44-88-132, diagTop+14, w-44-100, diagTop+44), narrow: narrow,
	}
	if narrow {
		l.save = logical(w-44-126, 360, w-44, 396)
	}
	return l
}

func (v *desktopView) geometry() desktopLayout {
	var r desktopRect
	desktopCall("GetClientRect", v.window, uintptr(unsafe.Pointer(&r)))
	return desktopMakeLayout(int(r.Right), int(r.Bottom), v.dpi)
}

func desktopMove(window uintptr, r desktopRect) {
	if window == 0 {
		return
	}
	desktopCall("MoveWindow", window, uintptr(r.Left), uintptr(r.Top), uintptr(maxInt(1, int(r.Right-r.Left))), uintptr(maxInt(1, int(r.Bottom-r.Top))), 1)
}

func (v *desktopView) layout() {
	if v.output == 0 {
		return
	}
	l := v.geometry()
	s := func(n int) int32 { return int32(desktopScale(n, v.dpi)) }
	desktopMove(v.buttons[0], l.main)
	desktopMove(v.buttons[2], l.diagnose)
	desktopMove(v.buttons[3], l.reload)
	desktopMove(v.saveSettings, l.save)
	desktopMove(v.copyOutput, l.copy)
	desktopMove(v.detailButton, l.detail)
	desktopMove(v.copyStatus, l.copyState)
	desktopMove(v.status, desktopRect{l.status.Left + s(36), l.status.Top + s(18), l.copyState.Left - s(12), l.status.Top + s(44)})
	for _, pair := range []struct {
		window uintptr
		r      desktopRect
	}{{v.upstreamHost, l.host}, {v.upstreamPort, l.port}} {
		r := pair.r
		r.Top += s(9)
		r.Bottom -= s(7)
		desktopMove(pair.window, r)
	}
	desktopMove(v.output, l.output)
	for i, field := range []desktopRect{l.host, l.port} {
		desktopMove(v.settingsLabels[i], desktopRect{field.Left, field.Top - s(23), field.Right, field.Top - s(3)})
	}
	desktopMove(desktopCall("GetDlgItem", v.window, 117), desktopRect{s(44), l.diagnosis.Top + s(15), s(194), l.diagnosis.Top + s(38)})
	desktopCall("ShowWindow", v.buttons[1], 0)
	v.updateOutputScrollbar()
}

func (v *desktopView) updateOutputScrollbar() {
	if v.output == 0 {
		return
	}
	var client desktopRect
	desktopCall("GetClientRect", v.output, uintptr(unsafe.Pointer(&client)))
	s := func(n int) int32 { return int32(desktopScale(n, v.dpi)) }
	format := desktopRect{s(10), s(6), client.Right - s(10), client.Bottom - s(6)}
	desktopCall("SendMessageW", v.output, 0xB4, 0, uintptr(unsafe.Pointer(&format)))
	lines := desktopCall("SendMessageW", v.output, 0xBA, 0, 0)
	hdc := desktopCall("GetDC", v.output)
	old, _, _ := desktopGDI.NewProc("SelectObject").Call(hdc, v.font)
	var metrics [16]int32
	desktopGDI.NewProc("GetTextMetricsW").Call(hdc, uintptr(unsafe.Pointer(&metrics[0])))
	desktopGDI.NewProc("SelectObject").Call(hdc, old)
	desktopCall("ReleaseDC", v.output, hdc)
	lineHeight := metrics[0]
	if lineHeight <= 0 {
		lineHeight = s(16)
	}
	desktopCall("ShowScrollBar", v.output, 1, boolUint(int32(lines)*lineHeight > format.Bottom-format.Top))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func desktopRound(hdc uintptr, r desktopRect, radius int, color, border uintptr) {
	brush, _, _ := desktopGDI.NewProc("CreateSolidBrush").Call(color)
	pen, _, _ := desktopGDI.NewProc("CreatePen").Call(0, 1, border)
	oldBrush, _, _ := desktopGDI.NewProc("SelectObject").Call(hdc, brush)
	oldPen, _, _ := desktopGDI.NewProc("SelectObject").Call(hdc, pen)
	desktopGDI.NewProc("RoundRect").Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(radius*2), uintptr(radius*2))
	desktopGDI.NewProc("SelectObject").Call(hdc, oldBrush)
	desktopGDI.NewProc("SelectObject").Call(hdc, oldPen)
	desktopGDI.NewProc("DeleteObject").Call(brush)
	desktopGDI.NewProc("DeleteObject").Call(pen)
}

func desktopLine(hdc uintptr, x, y, right int32, color uintptr) {
	pen, _, _ := desktopGDI.NewProc("CreatePen").Call(0, 1, color)
	old, _, _ := desktopGDI.NewProc("SelectObject").Call(hdc, pen)
	desktopGDI.NewProc("MoveToEx").Call(hdc, uintptr(x), uintptr(y), 0)
	desktopGDI.NewProc("LineTo").Call(hdc, uintptr(right), uintptr(y))
	desktopGDI.NewProc("SelectObject").Call(hdc, old)
	desktopGDI.NewProc("DeleteObject").Call(pen)
}

func desktopSingleText(hdc, font uintptr, text string, r desktopRect, color uintptr) {
	desktopGDI.NewProc("SelectObject").Call(hdc, font)
	desktopGDI.NewProc("SetTextColor").Call(hdc, color)
	desktopGDI.NewProc("SetBkMode").Call(hdc, 1)
	desktopCall("DrawTextW", hdc, uintptr(unsafe.Pointer(desktopString(text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x8824)
}

func (v *desktopView) paint() {
	var ps [72]byte
	hdc := desktopCall("BeginPaint", v.window, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer desktopCall("EndPaint", v.window, uintptr(unsafe.Pointer(&ps)))
	var client desktopRect
	desktopCall("GetClientRect", v.window, uintptr(unsafe.Pointer(&client)))
	desktopCall("FillRect", hdc, uintptr(unsafe.Pointer(&client)), v.backgroundBrush)
	l := v.geometry()
	s := func(n int) int32 { return int32(desktopScale(n, v.dpi)) }
	text := func(value string, x, y, width, height int32, font, color uintptr) {
		desktopSingleText(hdc, font, value, desktopRect{x, y, x + width, y + height}, color)
	}
	desktopRound(hdc, l.status, desktopScale(8, v.dpi), desktopSurface, desktopSurface)
	desktopRound(hdc, l.settings, desktopScale(8, v.dpi), desktopSurface, desktopSurface)
	desktopRound(hdc, l.diagnosis, desktopScale(8, v.dpi), desktopSurface, desktopSurface)
	text("路由助手", s(24), s(17), s(280), s(31), v.titleFont, desktopTextPrimary)
	text("本地应用的域名分流与连接状态", s(25), s(52), client.Right-s(170), s(20), v.labelFont, desktopTextSecondary)
	statusColor := desktopTextSecondary
	if v.panel.Running {
		statusColor = desktopSuccess
	}
	if (!v.panel.Running && v.panel.RestorePending) || v.panel.ConfigChanged {
		statusColor = desktopWarning
	}
	if v.busy && v.activeAction != 103 {
		statusColor = desktopAccent
	}
	desktopRound(hdc, desktopRect{s(44), s(115), s(52), s(123)}, desktopScale(4, v.dpi), statusColor, statusColor)
	text(v.panel.Subtitle, s(44), s(140), client.Right-s(88), s(18), v.labelFont, desktopTextSecondary)
	desktopLine(hdc, s(44), s(171), client.Right-s(44), desktopBorder)
	columnWidth := (client.Right - s(88)) / 3
	for i, column := range []struct{ label, value, detail string }{{"本地入口", v.panel.Entry, "应用连接通过此入口"}, {"上游健康", v.panel.Health, v.panel.HealthDetail}, {"最近应用连接", v.panel.Target, v.panel.TargetDetail}} {
		x := s(44) + int32(i)*columnWidth
		text(column.label, x, s(180), columnWidth-s(12), s(17), v.labelFont, desktopTextSecondary)
		valueColor := desktopTextPrimary
		if i == 1 && (strings.Contains(column.value, "过期") || strings.Contains(column.value, "未知")) {
			valueColor = desktopWarning
		}
		text(column.value, x, s(199), columnWidth-s(16), s(22), v.bodyFont, valueColor)
		detail := column.detail
		if i == 0 && !v.panel.Running {
			detail = "启动后台后可用"
		}
		text(detail, x, s(223), columnWidth-s(16), s(16), v.labelFont, desktopTextSecondary)
	}
	text("上游配置", s(44), l.settings.Top+s(10), s(160), s(24), v.bodyFont, desktopTextPrimary)
	focus := desktopCall("GetFocus")
	for _, field := range []struct {
		window uintptr
		r      desktopRect
	}{{v.upstreamHost, l.host}, {v.upstreamPort, l.port}} {
		border := desktopBorder
		if focus == field.window {
			border = desktopAccent
		}
		if v.settingsError == field.window {
			border = desktopFailure
		}
		desktopRound(hdc, field.r, desktopScale(5, v.dpi), desktopInput, border)
	}
	notice := v.settingsNotice
	if notice == "" {
		switch {
		case v.panel.Running:
			notice = "请先停止后台，再保存配置。"
		case v.dirty:
			notice = "存在未保存的修改，保存后下次启动生效。"
		default:
			notice = "配置将在下次启动后台时生效。"
		}
	}
	noticeColor := desktopTextSecondary
	if v.noticeFailed {
		noticeColor = desktopFailure
	} else if v.settingsNotice != "" {
		noticeColor = desktopSuccess
	}
	noticeY := l.host.Bottom + s(12)
	noticeWidth := client.Right - s(88)
	if l.narrow {
		noticeY = l.save.Top + s(7)
		noticeWidth = l.save.Left - s(60)
	}
	text(notice, s(44), noticeY, noticeWidth, s(24), v.labelFont, noticeColor)
	summary := "尚未运行诊断 · 检查配置、上游与本地入口"
	if v.diagnostic != nil {
		summary = v.diagnostic.Summary
	}
	if v.busy {
		summary = "正在执行操作，请稍候……"
	}
	summaryColor := desktopTextSecondary
	if v.diagnostic != nil && v.diagnostic.Failed {
		summaryColor = desktopFailure
	}
	text(summary, s(44), l.diagnosis.Top+s(45), client.Right-s(88), s(22), v.labelFont, summaryColor)
	frame := l.output
	frame.Left -= s(1)
	frame.Top -= s(1)
	frame.Right += s(1)
	frame.Bottom += s(1)
	desktopRound(hdc, frame, desktopScale(4, v.dpi), desktopInput, desktopInput)
	text("本地运行", s(25), client.Bottom-s(27), s(150), s(18), v.labelFont, desktopTextSecondary)
	updated := "正在读取状态"
	if !v.panel.Updated.IsZero() {
		updated = fmt.Sprintf("状态更新于 %s", v.panel.Updated.Format("15:04:05"))
	}
	text(updated, client.Right-s(218), client.Bottom-s(27), s(194), s(18), v.labelFont, desktopTextSecondary)
}

func (v *desktopView) drawButton(item *desktopDrawItem) {
	if item == nil || item.hDC == 0 {
		return
	}
	primary := item.CtlID == 101 && !v.panel.Running && !v.panel.RestorePending
	color, border, textColor := desktopSurface, desktopBorder, desktopTextPrimary
	if primary {
		color, border, textColor = desktopAccent, desktopAccent, rgb(7, 18, 32)
	}
	if item.hwndItem != 0 && item.hwndItem == v.hovered {
		color = rgb(48, 57, 70)
		if primary {
			color = rgb(111, 164, 255)
		}
		if item.CtlID == 101 && v.panel.Running {
			border, textColor = desktopFailure, desktopFailure
		}
	}
	if item.ItemState&1 != 0 {
		color = rgb(42, 71, 114)
	}
	if item.ItemState&4 != 0 {
		color, border, textColor = rgb(36, 41, 49), rgb(47, 54, 64), rgb(117, 129, 146)
	}
	background := v.panelBrush
	if item.CtlID == 103 {
		background = v.backgroundBrush
	}
	desktopCall("FillRect", item.hDC, uintptr(unsafe.Pointer(&item.rcItem)), background)
	r := item.rcItem
	r.Right--
	r.Bottom--
	desktopRound(item.hDC, r, desktopScale(5, v.dpi), color, border)
	label := ""
	if item.hwndItem != 0 {
		label = desktopGetText(item.hwndItem)
	}
	if label == "" {
		label = map[uint32]string{101: "启动后台", 102: "停止后台", 103: "运行诊断", 104: "重新载入配置", 109: "保存配置", 111: "复制全部", 112: "查看详细记录", 116: "复制状态"}[item.CtlID]
	}
	desktopGDI.NewProc("SelectObject").Call(item.hDC, v.bodyFont)
	desktopGDI.NewProc("SetTextColor").Call(item.hDC, textColor)
	desktopGDI.NewProc("SetBkMode").Call(item.hDC, 1)
	r = item.rcItem
	desktopCall("DrawTextW", item.hDC, uintptr(unsafe.Pointer(desktopString(label))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x825)
	if item.ItemState&0x10 != 0 && item.ItemState&0x100 == 0 {
		r.Left += int32(desktopScale(4, v.dpi))
		r.Top += int32(desktopScale(4, v.dpi))
		r.Right -= int32(desktopScale(4, v.dpi))
		r.Bottom -= int32(desktopScale(4, v.dpi))
		desktopCall("DrawFocusRect", item.hDC, uintptr(unsafe.Pointer(&r)))
	}
}

var desktopButtonCallback = syscall.NewCallback(desktopButtonProc)

func desktopButtonProc(window uintptr, message uint32, wparam, lparam uintptr) uintptr {
	v := desktopCurrent
	if v == nil || v.buttonProc == 0 {
		return desktopCall("DefWindowProcW", window, uintptr(message), wparam, lparam)
	}
	switch message {
	case 0x200:
		if v.hovered != window {
			previous := v.hovered
			v.hovered = window
			tme := struct {
				Size, Flags uint32
				Window      uintptr
				Hover       uint32
			}{Flags: 2, Window: window}
			tme.Size = uint32(unsafe.Sizeof(tme))
			desktopCall("TrackMouseEvent", uintptr(unsafe.Pointer(&tme)))
			if previous != 0 {
				desktopCall("InvalidateRect", previous, 0, 0)
			}
			desktopCall("InvalidateRect", window, 0, 0)
		}
	case 0x2A3:
		if v.hovered == window {
			v.hovered = 0
			desktopCall("InvalidateRect", window, 0, 0)
		}
	case 7, 8:
		desktopCall("InvalidateRect", v.window, 0, 0)
	}
	return desktopCall("CallWindowProcW", v.buttonProc, window, uintptr(message), wparam, lparam)
}

func desktopCopyText(window uintptr, text string) error {
	if desktopCall("OpenClipboard", window) == 0 {
		return fmt.Errorf("剪贴板正被占用，请重试。")
	}
	defer desktopCall("CloseClipboard")
	data := syscall.StringToUTF16(text)
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	handle, _, _ := kernel.NewProc("GlobalAlloc").Call(2, uintptr(len(data)*2))
	if handle == 0 {
		return fmt.Errorf("无法分配复制内容所需的内存。")
	}
	ptr, _, _ := kernel.NewProc("GlobalLock").Call(handle)
	if ptr == 0 {
		kernel.NewProc("GlobalFree").Call(handle)
		return fmt.Errorf("无法准备复制内容。")
	}
	kernel.NewProc("RtlMoveMemory").Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	kernel.NewProc("GlobalUnlock").Call(handle)
	desktopCall("EmptyClipboard")
	if desktopCall("SetClipboardData", 13, handle) == 0 {
		kernel.NewProc("GlobalFree").Call(handle)
		return fmt.Errorf("复制失败，请重试。")
	}
	return nil
}
