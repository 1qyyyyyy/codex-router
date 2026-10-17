//go:build windows

package main

import (
	"codex-router/config"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
	labelFont                              uintptr
	dpi                                    uint32
	backgroundBrush, panelBrush, editBrush uintptr
	buttons                                [4]uintptr
	settingsLabels                         [2]uintptr
	upstreamHost, upstreamPort             uintptr
	saveSettings, cancelSettings           uintptr
	settingsMode                           bool
	copyOutput, detailButton, copyStatus   uintptr
	panel                                  desktopPanelState
	diagnostic                             *desktopDiagnosisReport
	expanded                               bool
	outputText                             string
	savedHost, savedPort                   string
	settingsLoading, dirty                 bool
	settingsNotice                         string
	noticeFailed                           bool
	settingsError                          uintptr
	activeAction                           uintptr
	hovered                                uintptr
	buttonProc                             uintptr
	closed                                 bool
	path                                   string
	tray                                   desktopNotify
	restart                                uint32
	trayReady                              bool
	busy                                   bool
	refresh                                bool
	statusGeneration                       uint64
	mu                                     sync.Mutex
	results                                []desktopResult
}
type desktopResult struct {
	status     bool
	text       string
	panel      desktopPanelState
	report     *desktopDiagnosisReport
	generation uint64
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
		for attempt := 0; attempt < 40; attempt++ {
			window := desktopCall("FindWindowW", uintptr(unsafe.Pointer(desktopString(name))), 0)
			if window != 0 {
				desktopCall("PostMessageW", window, 0x8003, 0, 0)
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	}
	desktopEnableDPI()
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	class := desktopClass{Size: uint32(unsafe.Sizeof(desktopClass{})), Callback: syscall.NewCallback(desktopWindowProc), Instance: instance, Icon: desktopCall("LoadIconW", 0, 32512), Cursor: desktopCall("LoadCursorW", 0, 32512), Name: desktopString(name)}
	if desktopCall("RegisterClassExW", uintptr(unsafe.Pointer(&class))) == 0 {
		return fmt.Errorf("无法注册原生面板窗口")
	}
	defer desktopCall("UnregisterClassW", uintptr(unsafe.Pointer(class.Name)), instance)
	view := &desktopView{path: absolute, panel: desktopPanelState{Title: "正在读取后台状态", Subtitle: "正在确认本地入口和上游连接……", Entry: "正在读取", Health: "正在读取", Target: "正在读取"}}
	desktopCurrent = view
	defer func() { desktopCurrent = nil }()
	view.backgroundBrush, _, _ = desktopGDI.NewProc("CreateSolidBrush").Call(desktopBackground)
	view.panelBrush, _, _ = desktopGDI.NewProc("CreateSolidBrush").Call(desktopSurface)
	view.editBrush, _, _ = desktopGDI.NewProc("CreateSolidBrush").Call(desktopInput)
	view.restart = uint32(desktopCall("RegisterWindowMessageW", uintptr(unsafe.Pointer(desktopString("TaskbarCreated")))))
	window := desktopCall("CreateWindowExW", 0, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(desktopString("路由助手 · 状态面板"))), 0x02CF0000, 0x80000000, 0x80000000, 980, 760, 0, 0, instance, 0)
	if window == 0 {
		return fmt.Errorf("无法创建原生状态面板")
	}
	view.window = window
	view.rebuildFonts(desktopWindowDPI(window))
	desktopApplyTitleTheme(window)
	view.status = view.control("STATIC", view.panel.Title, 0x5000000C, 11)
	view.control("STATIC", "诊断记录", 0x50000000, 117)
	view.output = view.control("EDIT", "", 0x50010844, 12)
	view.settingsLabels[0] = view.control("STATIC", "上游主机地址", 0x50000000, 113)
	view.upstreamHost = view.control("EDIT", "", 0x50010080, 107)
	view.settingsLabels[1] = view.control("STATIC", "端口", 0x50000000, 114)
	view.upstreamPort = view.control("EDIT", "", 0x50012080, 108)
	desktopCall("SendMessageW", view.upstreamHost, 0xC5, 253, 0)
	desktopCall("SendMessageW", view.upstreamPort, 0xC5, 5, 0)
	desktopCall("SendMessageW", view.output, 0xC5, 1<<20, 0)
	for i, label := range []string{"启动后台", "停止后台", "运行诊断", "重新载入配置"} {
		view.buttons[i] = view.control("BUTTON", label, 0x5001000B, uintptr(101+i))
	}
	view.saveSettings = view.control("BUTTON", "保存配置", 0x5001000B, 109)
	view.copyOutput = view.control("BUTTON", "复制全部", 0x5001000B, 111)
	view.detailButton = view.control("BUTTON", "查看详细记录", 0x5001000B, 112)
	view.copyStatus = view.control("BUTTON", "复制状态", 0x5001000B, 116)
	view.rebuildFonts(view.dpi)
	view.tray = desktopNotify{Size: uint32(unsafe.Sizeof(desktopNotify{})), Window: window, ID: 1, Flags: 7, Callback: 0x8001, Icon: class.Icon}
	copy(view.tray.Tip[:], syscall.StringToUTF16("路由助手：后台状态与诊断"))
	view.addTray()
	view.openSettings()
	view.setOutput("尚未运行诊断。\r\n点击右上角“运行诊断”，检查配置、上游连接和本地代理状态。")
	view.updateControls()
	desktopFitInitialWindow(view)
	desktopCall("SetTimer", window, 1, 3000, 0)
	desktopCall("ShowWindow", window, 9)
	desktopCall("ShowWindow", window, 9)
	view.layout()
	desktopCall("UpdateWindow", window)
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
		if view.handleKey(message) {
			continue
		}
		if desktopCall("IsDialogMessageW", window, uintptr(unsafe.Pointer(&message))) != 0 {
			continue
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
		desktopCall("SendMessageW", h, 0xD3, 3, uintptr(desktopScale(10, v.dpi)|(desktopScale(10, v.dpi)<<16)))
	}
	if class == "BUTTON" {
		previous := desktopCall("SetWindowLongPtrW", h, ^uintptr(3), desktopButtonCallback)
		if v.buttonProc == 0 {
			v.buttonProc = previous
		}
	}
	return h
}

func desktopGetText(window uintptr) string {
	length := desktopCall("GetWindowTextLengthW", window)
	buffer := make([]uint16, length+1)
	desktopCall("GetWindowTextW", window, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer)
}

func (v *desktopView) handleKey(message desktopMessage) bool {
	if message.Message != 0x100 {
		return false
	}
	focus := desktopCall("GetFocus")
	if message.WParam == 9 {
		order := []uintptr{v.buttons[0], v.buttons[2], v.copyStatus, v.upstreamHost, v.upstreamPort, v.buttons[3], v.saveSettings, v.detailButton, v.copyOutput, v.output}
		position := -1
		for i, control := range order {
			if control == focus {
				position = i
				break
			}
		}
		direction := 1
		if desktopCall("GetKeyState", 0x10)&0x8000 != 0 {
			direction = -1
			if position < 0 {
				position = 0
			}
		}
		for attempt := 0; attempt < len(order); attempt++ {
			position = (position + direction + len(order)) % len(order)
			control := order[position]
			if control != 0 && desktopCall("IsWindowVisible", control) != 0 && desktopCall("IsWindowEnabled", control) != 0 {
				desktopCall("SendMessageW", v.window, 0x127, 0x10002, 0)
				desktopCall("SetFocus", control)
				return true
			}
		}
		return true
	}
	if message.WParam == 13 {
		if focus == v.upstreamHost || focus == v.upstreamPort {
			if desktopCall("IsWindowEnabled", v.saveSettings) != 0 {
				v.action(109)
			}
			return true
		}
		for _, button := range v.allButtons() {
			if focus == button && desktopCall("IsWindowEnabled", button) != 0 {
				v.action(desktopCall("GetDlgCtrlID", button))
				return true
			}
		}
	}
	return false
}

func (v *desktopView) allButtons() []uintptr {
	return append(v.buttons[:], v.saveSettings, v.copyOutput, v.detailButton, v.copyStatus)
}

func (v *desktopView) openSettings() {
	cfg, err := config.Load(v.path)
	if err != nil {
		v.settingsNotice = desktopConfigNotice(err)
		v.noticeFailed = true
		v.settingsError = v.upstreamHost
		v.updateControls()
		return
	}
	v.settingsLoading = true
	v.savedHost, v.savedPort = cfg.Upstream.Host, strconv.Itoa(cfg.Upstream.Port)
	desktopCall("SetWindowTextW", v.upstreamHost, uintptr(unsafe.Pointer(desktopString(v.savedHost))))
	desktopCall("SetWindowTextW", v.upstreamPort, uintptr(unsafe.Pointer(desktopString(v.savedPort))))
	v.settingsLoading = false
	v.dirty = false
	v.settingsError = 0
	v.settingsNotice = ""
	v.noticeFailed = false
	v.updateControls()
}

func (v *desktopView) settingsChanged() {
	if v.settingsLoading {
		return
	}
	v.dirty = strings.TrimSpace(desktopGetText(v.upstreamHost)) != v.savedHost || strings.TrimSpace(desktopGetText(v.upstreamPort)) != v.savedPort
	v.settingsError = 0
	v.settingsNotice = ""
	v.noticeFailed = false
	v.updateControls()
}

func (v *desktopView) settingsFailure(control uintptr, message string) {
	v.settingsError = control
	v.settingsNotice = message
	v.noticeFailed = true
	v.updateControls()
	if control != 0 {
		desktopCall("SetFocus", control)
		desktopCall("SendMessageW", control, 0xB1, 0, ^uintptr(0))
	}
}

func desktopValidateFields(host, portText string) (int, uintptr, string) {
	host = strings.TrimSpace(host)
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port < 1 || port > 65535 {
		return 0, 108, "端口范围为 1 至 65535。"
	}
	if host == "" || strings.ContainsAny(host, " /\\\t\r\n:@?#") && net.ParseIP(host) == nil {
		return 0, 107, "请输入有效的主机名或地址，不要包含协议或端口。"
	}
	return port, 0, ""
}

func (v *desktopView) saveSettingsValues() {
	if v.busy {
		return
	}
	if v.panel.Running {
		v.settingsFailure(0, "请先停止后台，再保存配置。")
		return
	}
	host := strings.TrimSpace(desktopGetText(v.upstreamHost))
	port, id, notice := desktopValidateFields(host, desktopGetText(v.upstreamPort))
	if id != 0 {
		control := v.upstreamHost
		if id == 108 {
			control = v.upstreamPort
		}
		v.settingsFailure(control, notice)
		return
	}
	// 与后台进程共用互斥锁，保证保存时没有进程正在启动或管理代理。
	release, lockErr := lockBackground()
	if lockErr != nil {
		v.settingsFailure(0, "后台正在运行或切换状态，请先停止后台再保存。")
		return
	}
	defer release()
	statePath, _ := backgroundPaths(v.path)
	if _, err := readBackground(statePath); !errors.Is(err, os.ErrNotExist) {
		v.settingsFailure(0, "后台状态尚未清理，请先执行停止或恢复代理设置。")
		v.readStatus()
		return
	}
	if err := config.UpdateUpstream(v.path, host, port); err != nil {
		v.settingsFailure(v.upstreamHost, desktopConfigNotice(err))
		return
	}
	v.savedHost, v.savedPort = host, strconv.Itoa(port)
	v.settingsLoading = true
	desktopCall("SetWindowTextW", v.upstreamPort, uintptr(unsafe.Pointer(desktopString(v.savedPort))))
	v.settingsLoading = false
	v.dirty = false
	v.settingsError = 0
	v.settingsNotice = "已保存，下次启动后台时生效。"
	v.noticeFailed = false
	v.updateControls()
	v.readStatus()
}

func desktopConfigNotice(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "配置文件不存在，请确认文件位置后重新载入。"
	}
	if errors.Is(err, os.ErrPermission) {
		return "无法读写配置，请检查文件访问权限。"
	}
	var configErr *config.ConfigError
	if errors.As(err, &configErr) {
		return configErr.Message
	}
	return "配置操作失败，请检查配置文件内容和访问权限。"
}

func (v *desktopView) updateControls() {
	if v.window == 0 {
		return
	}
	label := "启动后台"
	if v.panel.Running {
		label = "停止后台"
	} else if v.panel.RestorePending {
		label = "恢复代理设置"
	}
	if v.activeAction == 101 {
		label = "正在启动…"
	} else if v.activeAction == 102 {
		label = "正在停止…"
	}
	desktopCall("SetWindowTextW", v.buttons[0], uintptr(unsafe.Pointer(desktopString(label))))
	desktopCall("SetWindowTextW", v.status, uintptr(unsafe.Pointer(desktopString(v.panel.Title))))
	for _, b := range v.buttons {
		desktopCall("EnableWindow", b, boolUint(!v.busy))
	}
	desktopCall("ShowWindow", v.buttons[1], 0)
	desktopCall("EnableWindow", v.saveSettings, boolUint(v.dirty && !v.busy && !v.panel.Running))
	desktopCall("EnableWindow", v.upstreamHost, boolUint(!v.busy))
	desktopCall("EnableWindow", v.upstreamPort, boolUint(!v.busy))
	desktopCall("EnableWindow", v.detailButton, boolUint(!v.busy && v.diagnostic != nil))
	desktopCall("EnableWindow", v.copyOutput, boolUint(v.outputText != ""))
	for _, b := range v.allButtons() {
		desktopCall("InvalidateRect", b, 0, 1)
	}
	desktopCall("SetWindowTextW", v.copyOutput, uintptr(unsafe.Pointer(desktopString("复制全部"))))
	desktopCall("SetWindowTextW", v.copyStatus, uintptr(unsafe.Pointer(desktopString("复制状态"))))
	desktopCall("InvalidateRect", v.window, 0, 0)
}

func boolUint(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func (v *desktopView) setOutput(text string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	if text == v.outputText {
		return
	}
	v.outputText = text
	desktopCall("SetWindowTextW", v.output, uintptr(unsafe.Pointer(desktopString(text))))
	v.updateOutputScrollbar()
}

func (v *desktopView) showDiagnosis() {
	if v.diagnostic == nil {
		return
	}
	label := "查看详细记录"
	text := desktopCompactDiagnosis(*v.diagnostic)
	if v.expanded {
		label = "收起详细记录"
		text = v.diagnostic.Detail
	}
	desktopCall("SetWindowTextW", v.detailButton, uintptr(unsafe.Pointer(desktopString(label))))
	v.setOutput(text)
}

func desktopCompactDiagnosis(report desktopDiagnosisReport) string {
	var b strings.Builder
	for _, step := range report.Steps {
		fmt.Fprintf(&b, "%s  %s", step.Status, step.Name)
		if step.DurationMS > 0 {
			fmt.Fprintf(&b, "  ·  %d 毫秒", step.DurationMS)
		}
		b.WriteString("\n")
		if step.Status != "通过" && step.Advice != "" {
			b.WriteString("      建议：" + step.Advice + "\n")
		}
	}
	if len(report.Steps) == 0 {
		b.WriteString(report.Detail)
	}
	return b.String()
}

func (v *desktopView) addTray() {
	result, _, _ := desktopShell.NewProc("Shell_NotifyIconW").Call(0, uintptr(unsafe.Pointer(&v.tray)))
	v.trayReady = result != 0
}

func (v *desktopView) publish(r desktopResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	v.results = append(v.results, r)
	desktopCall("PostMessageW", v.window, 0x8002, 0, 0)
}

func (v *desktopView) readStatus() {
	if v.refresh {
		return
	}
	v.refresh = true
	generation := v.statusGeneration
	go func() {
		v.publish(desktopResult{status: true, panel: readDesktopPanelState(v.path), generation: generation})
	}()
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
	if id == 111 || id == 116 {
		text := v.outputText
		if id == 111 && v.diagnostic != nil {
			text = v.diagnostic.Detail
		}
		if id == 116 {
			text = fmt.Sprintf("%s\n%s\n本地入口：%s\n上游健康：%s\n%s\n最近应用连接：%s\n%s", v.panel.Title, v.panel.Subtitle, v.panel.Entry, v.panel.Health, v.panel.HealthDetail, v.panel.Target, v.panel.TargetDetail)
		}
		if err := desktopCopyText(v.window, text); err != nil {
			v.settingsNotice = err.Error()
			v.noticeFailed = true
		} else if id == 111 {
			if v.noticeFailed {
				v.settingsNotice = ""
				v.noticeFailed = false
			}
			desktopCall("SetWindowTextW", v.copyOutput, uintptr(unsafe.Pointer(desktopString("已复制"))))
		} else {
			if v.noticeFailed {
				v.settingsNotice = ""
				v.noticeFailed = false
			}
			desktopCall("SetWindowTextW", v.copyStatus, uintptr(unsafe.Pointer(desktopString("已复制"))))
		}
		desktopCall("InvalidateRect", v.window, 0, 0)
		return
	}
	if v.busy {
		return
	}
	if id == 112 {
		v.expanded = !v.expanded
		v.showDiagnosis()
		v.updateControls()
		return
	}
	if id == 104 {
		if v.dirty && desktopCall("MessageBoxW", v.window, uintptr(unsafe.Pointer(desktopString("重新载入会覆盖尚未保存的修改，是否继续？"))), uintptr(unsafe.Pointer(desktopString("重新载入配置"))), 0x124) != 6 {
			return
		}
		v.openSettings()
		v.readStatus()
		return
	}
	if id == 109 {
		v.saveSettingsValues()
		return
	}
	if id == 101 && (v.panel.Running || v.panel.RestorePending) {
		id = 102
	}
	if id != 101 && id != 102 && id != 103 {
		return
	}
	v.busy = true
	v.statusGeneration++
	v.activeAction = id
	v.diagnostic = nil
	v.expanded = false
	label := "正在执行，请稍候……"
	if id == 103 {
		label = "正在运行六步诊断，请稍候……"
	}
	v.setOutput(label)
	v.updateControls()
	go func(action uintptr) {
		if action == 103 {
			report := runDesktopDiagnosis(v.path)
			v.publish(desktopResult{report: &report})
			return
		}
		name := "start"
		if action == 102 {
			name = "stop"
		}
		result, err := desktopAction(name, v.path)
		if err != nil {
			result += "\n操作失败：" + err.Error()
		}
		v.publish(desktopResult{text: result})
	}(id)
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
		dpi := v.dpi
		if dpi == 0 {
			dpi = 96
		}
		r := desktopRect{0, 0, int32(desktopScale(680, dpi)), int32(desktopScale(620, dpi))}
		adjust := desktopUser.NewProc("AdjustWindowRectExForDpi")
		if adjust.Find() == nil {
			adjust.Call(uintptr(unsafe.Pointer(&r)), 0x02CF0000, 0, 0, uintptr(dpi))
		} else {
			desktopCall("AdjustWindowRectEx", uintptr(unsafe.Pointer(&r)), 0x02CF0000, 0, 0)
		}
		minimum := desktopPoint{r.Right - r.Left, r.Bottom - r.Top}
		windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory").Call(lparam+unsafe.Offsetof(desktopMinMax{}.MinimumTrack), uintptr(unsafe.Pointer(&minimum)), unsafe.Sizeof(minimum))
		return 0
	case 0x2E0:
		desktopApplyDPIChange(v, wparam, lparam)
		return 0
	case 0x1A, 0x31A:
		desktopApplyTitleTheme(window)
		desktopCall("InvalidateRect", window, 0, 1)
		return 0
	case 0xF:
		v.paint()
		return 0
	case 0x14:
		return 1
	case 0x133, 0x134, 0x135, 0x138:
		color, brush := desktopInput, v.editBrush
		if lparam == v.status || lparam == v.settingsLabels[0] || lparam == v.settingsLabels[1] || lparam == desktopCall("GetDlgItem", window, 117) {
			color, brush = desktopSurface, v.panelBrush
		}
		desktopGDI.NewProc("SetTextColor").Call(wparam, desktopTextPrimary)
		desktopGDI.NewProc("SetBkColor").Call(wparam, color)
		return brush
	case 0x2B:
		if lparam != 0 {
			var item desktopDrawItem
			windows.NewLazySystemDLL("kernel32.dll").NewProc("RtlMoveMemory").Call(uintptr(unsafe.Pointer(&item)), lparam, unsafe.Sizeof(item))
			v.drawButton(&item)
		}
		return 1
	case 5:
		if v.output != 0 {
			v.layout()
			desktopCall("InvalidateRect", window, 0, 0)
		}
		return 0
	case 0x111:
		id := wparam & 0xFFFF
		if (id == 107 || id == 108) && wparam>>16 == 0x300 {
			v.settingsChanged()
			return 0
		}
		if (id == 107 || id == 108) && (wparam>>16 == 0x100 || wparam>>16 == 0x200) {
			desktopCall("InvalidateRect", window, 0, 0)
			return 0
		}
		if wparam>>16 == 0 {
			v.action(id)
		}
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
		v.mu.Lock()
		v.closed = true
		v.mu.Unlock()
		for _, object := range []uintptr{v.font, v.titleFont, v.bodyFont, v.labelFont, v.backgroundBrush, v.panelBrush, v.editBrush} {
			if object != 0 {
				desktopGDI.NewProc("DeleteObject").Call(object)
			}
		}
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
		needRefresh := false
		for _, r := range results {
			if r.status {
				v.refresh = false
				if r.generation == v.statusGeneration {
					v.panel = r.panel
				} else {
					needRefresh = true
				}
			} else {
				v.busy = false
				v.statusGeneration++
				v.activeAction = 0
				needRefresh = true
				if r.report != nil {
					v.diagnostic = r.report
					v.showDiagnosis()
				} else {
					v.setOutput(r.text)
				}
			}
		}
		v.updateControls()
		if needRefresh {
			v.readStatus()
		}
		return 0
	case 0x8001:
		if lparam == 0x203 {
			v.action(105)
			return 0
		}
		if lparam == 0x205 || lparam == 0x7B {
			menu := desktopCall("CreatePopupMenu")
			defer desktopCall("DestroyMenu", menu)
			for _, entry := range []struct {
				id   uintptr
				text string
			}{{105, "显示状态面板"}, {101, "启动后台"}, {102, "停止后台并恢复代理"}, {103, "运行诊断"}, {104, "重新载入配置"}, {106, "退出面板（后台继续运行）"}} {
				flags := uintptr(0)
				if (v.busy && entry.id >= 101 && entry.id <= 104) || (entry.id == 101 && (v.panel.Running || v.panel.RestorePending)) {
					flags = 1
				}
				desktopCall("AppendMenuW", menu, flags, entry.id, uintptr(unsafe.Pointer(desktopString(entry.text))))
			}
			var point desktopPoint
			desktopCall("GetCursorPos", uintptr(unsafe.Pointer(&point)))
			desktopCall("SetForegroundWindow", window)
			id := desktopCall("TrackPopupMenu", menu, 0x102, uintptr(point.X), uintptr(point.Y), 0, window, 0)
			if id != 0 {
				v.action(id)
			}
			desktopCall("PostMessageW", window, 0, 0, 0)
		}
		return 0
	}
	return desktopCall("DefWindowProcW", window, uintptr(message), wparam, lparam)
}
