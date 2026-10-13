//go:build windows

package main

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var desktopUser = windows.NewLazySystemDLL("user32.dll")
var desktopShell = windows.NewLazySystemDLL("shell32.dll")
var desktopGDI = windows.NewLazySystemDLL("gdi32.dll")

//go:uintptrescapes
func desktopCall(name string, args ...uintptr) uintptr {
	r, _, _ := desktopUser.NewProc(name).Call(args...)
	return r
}
func desktopString(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

type desktopPoint struct{ X, Y int32 }
type desktopMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          desktopPoint
	Private        uint32
}
type desktopClass struct {
	Size, Style                        uint32
	Callback                           uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type desktopNotify struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Timeout             uint32
	Title               [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	Balloon             uintptr
}
type desktopMinMax struct{ Reserved, Maximum, MaximumPosition, MinimumTrack, MaximumTrack desktopPoint }
type desktopView struct {
	window, status, output, font           uintptr
	titleFont, bodyFont                    uintptr
	backgroundBrush, panelBrush, editBrush uintptr
	buttons                                [4]uintptr
	path                                   string
	tray                                   desktopNotify
	restart                                uint32
	trayReady                              bool
	busy                                   bool
	refresh                                bool
	mu                                     sync.Mutex
	results                                []desktopResult
}
type desktopResult struct {
	status bool
	text   string
}

var desktopCurrent *desktopView

func rgb(r, g, b byte) uintptr { return uintptr(uint32(r) | uint32(g)<<8 | uint32(b)<<16) }

type desktopRect struct{ Left, Top, Right, Bottom int32 }
type desktopDrawItem struct {
	CtlType, CtlID, ItemID, ItemAction, ItemState uint32
	hwndItem, hDC                                 uintptr
	rcItem                                        desktopRect
	itemData                                      uintptr
}

func desktopText(hdc uintptr, font uintptr, text string, x, y, width, height int32, color uintptr) {
	desktopGDI.NewProc("SelectObject").Call(hdc, font)
	desktopGDI.NewProc("SetTextColor").Call(hdc, color)
	desktopGDI.NewProc("SetBkMode").Call(hdc, 1)
	rect := desktopRect{x, y, x + width, y + height}
	desktopUser.NewProc("DrawTextW").Call(hdc, uintptr(unsafe.Pointer(desktopString(text))), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), 0x0010)
}

func runDesktop(path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(absolute)))
	name := fmt.Sprintf("CodexRouterDesktop_%x", sum[:8])
	mutex, err := windows.CreateMutex(nil, false, desktopString("Local\\"+name))
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("无法创建面板实例锁：%w", err)
	}
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		window := uintptr(0)
		for attempt := 0; attempt < 40; attempt++ {
			window = desktopCall("FindWindowW", uintptr(unsafe.Pointer(desktopString(name))), 0)
			if window != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if window != 0 {
			desktopCall("PostMessageW", window, 0x8003, 0, 0)
		}
		return nil
	}
	// 使用系统自动缩放，确保高分辨率下文字和控件保持可读。
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	class := desktopClass{Size: uint32(unsafe.Sizeof(desktopClass{})), Callback: syscall.NewCallback(desktopWindowProc), Instance: uintptr(instance), Icon: desktopCall("LoadIconW", 0, 32512), Cursor: desktopCall("LoadCursorW", 0, 32512), Background: 6, Name: desktopString(name)}
	if desktopCall("RegisterClassExW", uintptr(unsafe.Pointer(&class))) == 0 {
		return fmt.Errorf("无法注册原生面板窗口")
	}
	defer desktopCall("UnregisterClassW", uintptr(unsafe.Pointer(class.Name)), uintptr(instance))
	view := &desktopView{path: absolute}
	desktopCurrent = view
	defer func() { desktopCurrent = nil }()
	view.restart = uint32(desktopCall("RegisterWindowMessageW", uintptr(unsafe.Pointer(desktopString("TaskbarCreated")))))
	window := desktopCall("CreateWindowExW", 0, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(desktopString("路由助手 · 状态面板"))), 0x00CF0000, 0x80000000, 0x80000000, 980, 760, 0, 0, uintptr(instance), 0)
	if window == 0 {
		return fmt.Errorf("无法创建原生状态面板")
	}
	view.window = window
	view.font, _, _ = desktopGDI.NewProc("CreateFontW").Call(^uintptr(15), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(desktopString("微软雅黑"))))
	view.titleFont, _, _ = desktopGDI.NewProc("CreateFontW").Call(^uintptr(25), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(desktopString("微软雅黑"))))
	view.bodyFont, _, _ = desktopGDI.NewProc("CreateFontW").Call(^uintptr(12), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(desktopString("微软雅黑"))))
	view.backgroundBrush, _, _ = desktopGDI.NewProc("CreateSolidBrush").Call(rgb(9, 15, 25))
	view.panelBrush, _, _ = desktopGDI.NewProc("CreateSolidBrush").Call(rgb(18, 29, 45))
	view.editBrush, _, _ = desktopGDI.NewProc("CreateSolidBrush").Call(rgb(15, 24, 38))
	view.status = view.control("EDIT", "正在读取后台状态……", 0x50A00844, 11)
	view.output = view.control("EDIT", "点击“一键检测”查看六步诊断和建议。这里的文字可选中并复制。", 0x50A00844, 12)
	labels := []string{"启动后台", "停止后台", "一键检测", "设置"}
	for i, label := range labels {
		view.buttons[i] = view.control("BUTTON", label, 0x50010000|0x0000000B, uintptr(101+i))
	}
	view.tray = desktopNotify{Size: uint32(unsafe.Sizeof(desktopNotify{})), Window: window, ID: 1, Flags: 7, Callback: 0x8001, Icon: class.Icon}
	copy(view.tray.Tip[:], syscall.StringToUTF16("路由助手：后台状态与诊断"))
	view.addTray()
	desktopCall("SetTimer", window, 1, 3000, 0)
	desktopCall("ShowWindow", window, 9)
	// 首次显示可能被启动器的隐藏标志覆盖，第二次调用明确展示面板。
	desktopCall("ShowWindow", window, 9)
	desktopCall("UpdateWindow", window)
	view.layout()
	view.readStatus()
	var message desktopMessage
	for {
		result := desktopCall("GetMessageW", uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if result == 0 {
			break
		}
		if int32(result) == -1 {
			return fmt.Errorf("面板消息处理失败")
		}
		desktopCall("TranslateMessage", uintptr(unsafe.Pointer(&message)))
		desktopCall("DispatchMessageW", uintptr(unsafe.Pointer(&message)))
	}
	runtime.KeepAlive(class)
	return nil
}
func (v *desktopView) control(class, text string, style, id uintptr) uintptr {
	h := desktopCall("CreateWindowExW", 0, uintptr(unsafe.Pointer(desktopString(class))), uintptr(unsafe.Pointer(desktopString(text))), style, 0, 0, 0, 0, v.window, id, 0, 0)
	desktopCall("SendMessageW", h, 0x30, v.bodyFont, 1)
	if class == "EDIT" {
		desktopCall("SendMessageW", h, 0x00D3, 3, uintptr(12|(12<<16)))
	}
	return h
}
func (v *desktopView) addTray() {
	result, _, _ := desktopShell.NewProc("Shell_NotifyIconW").Call(0, uintptr(unsafe.Pointer(&v.tray)))
	v.trayReady = result != 0
}

func (v *desktopView) paint() {
	var ps [72]byte
	hdc, _, _ := desktopUser.NewProc("BeginPaint").Call(v.window, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer desktopUser.NewProc("EndPaint").Call(v.window, uintptr(unsafe.Pointer(&ps)))
	var rect desktopRect
	desktopCall("GetClientRect", v.window, uintptr(unsafe.Pointer(&rect)))
	desktopUser.NewProc("FillRect").Call(hdc, uintptr(unsafe.Pointer(&rect)), v.backgroundBrush)
	width := rect.Right - rect.Left
	desktopText(hdc, v.titleFont, "路由助手", 24, 18, width-48, 34, rgb(242, 246, 252))
	desktopText(hdc, v.bodyFont, "本地应用的域名分流与运行状态", 26, 53, width-52, 20, rgb(145, 160, 181))
	desktopText(hdc, v.bodyFont, "运行概览", 24, 75, 120, 20, rgb(122, 190, 255))
	desktopText(hdc, v.bodyFont, "操作记录", 24, 315, 120, 20, rgb(122, 190, 255))
	desktopText(hdc, v.bodyFont, "路由助手 · 本地运行", 24, rect.Bottom-24, width-48, 18, rgb(91, 108, 132))
}

func (v *desktopView) drawButton(item *desktopDrawItem) {
	if item == nil || item.hDC == 0 {
		return
	}
	colors := map[uint32][3]byte{
		101: {52, 174, 122},
		102: {185, 82, 93},
		103: {62, 132, 205},
		104: {43, 59, 80},
	}
	color := colors[item.CtlID]
	// 按下和禁用状态都使用较深的底色，避免系统主题在按钮上绘出
	// 白色高光，保证深色面板中的层级关系稳定。
	if item.ItemState&1 != 0 {
		color = [3]byte{35, 48, 66}
	} else if item.ItemState&4 != 0 {
		color = [3]byte{34, 44, 58}
	}
	brush, _, _ := desktopGDI.NewProc("CreateSolidBrush").Call(rgb(color[0], color[1], color[2]))
	defer desktopGDI.NewProc("DeleteObject").Call(brush)
	desktopUser.NewProc("FillRect").Call(item.hDC, uintptr(unsafe.Pointer(&item.rcItem)), brush)
	borderColor := rgb(lighten(color[0]), lighten(color[1]), lighten(color[2]))
	if item.ItemState&4 != 0 {
		borderColor = rgb(66, 79, 98)
	}
	border, _, _ := desktopGDI.NewProc("CreatePen").Call(0, 1, borderColor)
	defer desktopGDI.NewProc("DeleteObject").Call(border)
	previousPen, _, _ := desktopGDI.NewProc("SelectObject").Call(item.hDC, border)
	defer desktopGDI.NewProc("SelectObject").Call(item.hDC, previousPen)
	previousBrush, _, _ := desktopGDI.NewProc("SelectObject").Call(item.hDC, brush)
	defer desktopGDI.NewProc("SelectObject").Call(item.hDC, previousBrush)
	desktopGDI.NewProc("Rectangle").Call(item.hDC, uintptr(item.rcItem.Left), uintptr(item.rcItem.Top), uintptr(item.rcItem.Right), uintptr(item.rcItem.Bottom))
	label := map[uint32]string{101: "启动后台", 102: "停止后台", 103: "一键检测", 104: "设置"}[item.CtlID]
	textColor := rgb(245, 248, 252)
	if item.ItemState&4 != 0 {
		textColor = rgb(133, 147, 166)
	}
	// 按钮文字水平、垂直居中，避免不同 DPI 下固定偏移造成视觉漂移。
	desktopGDI.NewProc("SelectObject").Call(item.hDC, v.bodyFont)
	desktopGDI.NewProc("SetTextColor").Call(item.hDC, textColor)
	desktopGDI.NewProc("SetBkMode").Call(item.hDC, 1)
	textRect := item.rcItem
	desktopUser.NewProc("DrawTextW").Call(item.hDC, uintptr(unsafe.Pointer(desktopString(label))), ^uintptr(0), uintptr(unsafe.Pointer(&textRect)), 0x0025)
}

func lighten(v byte) byte {
	if v > 237 {
		return 255
	}
	return v + 18
}
func (v *desktopView) layout() {
	var rect [4]int32
	desktopCall("GetClientRect", v.window, uintptr(unsafe.Pointer(&rect)))
	w, h := int(rect[2]), int(rect[3])
	top := 238
	desktopCall("MoveWindow", v.status, 24, 84, uintptr(w-48), 134, 1)
	for i, b := range v.buttons {
		width := 142
		gap := 12
		desktopCall("MoveWindow", b, uintptr(24+i*(width+gap)), uintptr(top), uintptr(width), 42, 1)
	}
	desktopCall("MoveWindow", v.output, 24, 334, uintptr(w-48), uintptr(h-370), 1)
}
func (v *desktopView) publish(r desktopResult) {
	v.mu.Lock()
	v.results = append(v.results, r)
	v.mu.Unlock()
	desktopCall("PostMessageW", v.window, 0x8002, 0, 0)
}
func (v *desktopView) readStatus() {
	if v.refresh {
		return
	}
	v.refresh = true
	go func() { v.publish(desktopResult{status: true, text: desktopStatus(v.path)}) }()
}
func (v *desktopView) action(id uintptr) {
	if id == 105 {
		desktopCall("ShowWindow", v.window, 9)
		desktopCall("SetForegroundWindow", v.window)
		return
	}
	if id == 106 {
		if v.busy {
			desktopCall("MessageBoxW", v.window, uintptr(unsafe.Pointer(desktopString("操作仍在进行，请完成后退出面板。"))), uintptr(unsafe.Pointer(desktopString("请稍候"))), 0x40)
			return
		}
		desktopCall("DestroyWindow", v.window)
		return
	}
	if v.busy {
		return
	}
	if id == 104 {
		desktopCall("MessageBoxW", v.window, uintptr(unsafe.Pointer(desktopString("请先停止后台，再修改配置，保存后重新启动后台。"))), uintptr(unsafe.Pointer(desktopString("设置提示"))), 0x40)
		command := exec.Command("notepad.exe", v.path)
		// 编辑器是用户明确请求的可见窗口。
		if err := command.Start(); err != nil {
			desktopCall("SetWindowTextW", v.output, uintptr(unsafe.Pointer(desktopString("无法打开配置文件："+err.Error()))))
		} else {
			go command.Wait()
		}
		return
	}
	actions := map[uintptr]string{101: "start", 102: "stop", 103: "diagnose"}
	action, ok := actions[id]
	if !ok {
		return
	}
	v.busy = true
	for _, b := range v.buttons {
		desktopCall("EnableWindow", b, 0)
	}
	desktopCall("SetWindowTextW", v.output, uintptr(unsafe.Pointer(desktopString("正在执行，请稍候……"))))
	go func() {
		result, err := desktopAction(action, v.path)
		if err != nil {
			result += "\r\n操作失败：" + err.Error()
		}
		v.publish(desktopResult{text: result})
	}()
}
func desktopWindowProc(window uintptr, message uint32, wparam, lparam uintptr) uintptr {
	v := desktopCurrent
	if v == nil {
		return desktopCall("DefWindowProcW", window, uintptr(message), wparam, lparam)
	}
	if message == v.restart && v.restart != 0 {
		v.addTray()
		return 0
	}
	switch message {
	case 0x24:
		// 仅写入最小尺寸字段，保留系统提供的最大尺寸，避免整数消息参数参与指针转换。
		minimum := desktopPoint{680, 560}
		windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory").Call(lparam+unsafe.Offsetof(desktopMinMax{}.MinimumTrack), uintptr(unsafe.Pointer(&minimum)), unsafe.Sizeof(minimum))
		return 0
	case 0x0f:
		v.paint()
		return 0
	case 0x14:
		return 1
	case 0x133, 0x134, 0x135, 0x138:
		hdc := wparam
		desktopGDI.NewProc("SetTextColor").Call(hdc, rgb(226, 234, 245))
		brush := v.editBrush
		// 状态区使用略亮的面板底色，与下方诊断记录形成清晰层级。
		if lparam == v.status {
			brush = v.panelBrush
			desktopGDI.NewProc("SetTextColor").Call(hdc, rgb(218, 231, 246))
		}
		backgroundColor := rgb(15, 24, 38)
		if lparam == v.status {
			backgroundColor = rgb(18, 29, 45)
		}
		desktopGDI.NewProc("SetBkColor").Call(hdc, backgroundColor)
		desktopGDI.NewProc("SelectObject").Call(hdc, brush)
		return brush
	case 0x2b:
		if lparam != 0 {
			// 将系统绘图消息复制到本地结构，避免整数直接转换为指针。
			var item desktopDrawItem
			windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory").Call(uintptr(unsafe.Pointer(&item)), lparam, unsafe.Sizeof(item))
			v.drawButton(&item)
		}
		return 1
	case 5:
		if v.status != 0 {
			v.layout()
		}
		return 0
	case 0x111:
		v.action(wparam & 0xffff)
		return 0
	case 0x113:
		v.readStatus()
		return 0
	case 0x10:
		if !v.trayReady {
			v.addTray()
			if !v.trayReady {
				desktopCall("MessageBoxW", window, uintptr(unsafe.Pointer(desktopString("托盘图标暂时不可用，面板将保持显示。请稍后再试。"))), uintptr(unsafe.Pointer(desktopString("无法隐藏面板"))), 0x30)
				return 0
			}
		}
		desktopCall("ShowWindow", window, 0)
		v.tray.Flags = 0x17
		copy(v.tray.Info[:], syscall.StringToUTF16("面板已隐藏到托盘，后台继续运行。右键托盘可显示面板或停止后台。"))
		copy(v.tray.Title[:], syscall.StringToUTF16("后台继续运行"))
		v.tray.InfoFlags = 1
		desktopShell.NewProc("Shell_NotifyIconW").Call(1, uintptr(unsafe.Pointer(&v.tray)))
		v.tray.Flags = 7
		return 0
	case 2:
		desktopGDI.NewProc("DeleteObject").Call(v.font)
		desktopGDI.NewProc("DeleteObject").Call(v.titleFont)
		desktopGDI.NewProc("DeleteObject").Call(v.bodyFont)
		desktopGDI.NewProc("DeleteObject").Call(v.backgroundBrush)
		desktopGDI.NewProc("DeleteObject").Call(v.panelBrush)
		desktopGDI.NewProc("DeleteObject").Call(v.editBrush)
		desktopCall("KillTimer", window, 1)
		desktopShell.NewProc("Shell_NotifyIconW").Call(2, uintptr(unsafe.Pointer(&v.tray)))
		desktopCall("PostQuitMessage", 0)
		return 0
	case 0x8003:
		v.action(105)
		return 0
	case 0x8002:
		v.mu.Lock()
		results := v.results
		v.results = nil
		v.mu.Unlock()
		for _, r := range results {
			target := v.output
			if r.status {
				target = v.status
				v.refresh = false
			} else {
				v.busy = false
				for _, b := range v.buttons {
					desktopCall("EnableWindow", b, 1)
				}
			}
			desktopCall("SetWindowTextW", target, uintptr(unsafe.Pointer(desktopString(strings.ReplaceAll(strings.ReplaceAll(r.text, "\r\n", "\n"), "\n", "\r\n")))))
		}
		return 0
	case 0x8001:
		if lparam == 0x203 {
			v.action(105)
			return 0
		}
		if lparam == 0x205 || lparam == 0x7b {
			menu := desktopCall("CreatePopupMenu")
			defer desktopCall("DestroyMenu", menu)
			for _, entry := range []struct {
				id   uintptr
				text string
			}{{105, "显示状态面板"}, {101, "启动后台"}, {102, "停止后台"}, {103, "一键检测"}, {104, "设置"}, {106, "退出面板（后台继续运行）"}} {
				flags := uintptr(0)
				if v.busy && entry.id >= 101 && entry.id <= 104 {
					flags = 1
				}
				desktopCall("AppendMenuW", menu, flags, entry.id, uintptr(unsafe.Pointer(desktopString(entry.text))))
			}
			var point desktopPoint
			desktopCall("GetCursorPos", uintptr(unsafe.Pointer(&point)))
			desktopCall("SetForegroundWindow", window)
			id := desktopCall("TrackPopupMenu", menu, 0x100|2, uintptr(point.X), uintptr(point.Y), 0, window, 0)
			if id != 0 {
				v.action(id)
			}
			desktopCall("PostMessageW", window, 0, 0, 0)
		}
		return 0
	}
	return desktopCall("DefWindowProcW", window, uintptr(message), wparam, lparam)
}
