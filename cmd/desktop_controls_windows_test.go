//go:build windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"codex-router/config"
)

func TestDesktopSettingsNativeControls(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	path := filepath.Join(t.TempDir(), "router.yaml")
	if err := config.WriteDefault(path); err != nil {
		t.Fatalf("创建临时配置失败：%v", err)
	}

	previous := desktopCurrent
	parent := desktopCall("CreateWindowExW", 0,
		uintptr(unsafe.Pointer(desktopString("STATIC"))),
		uintptr(unsafe.Pointer(desktopString("集成测试窗口"))),
		0, 0, 0, 640, 480, 0, 0, 0, 0)
	if parent == 0 {
		t.Fatal("无法创建隐藏的原生测试窗口")
	}
	view := &desktopView{window: parent, path: path}
	defer func() {
		desktopCurrent = previous
		desktopCall("DestroyWindow", parent)
		for _, font := range []uintptr{view.font, view.titleFont, view.bodyFont, view.labelFont} {
			if font != 0 {
				desktopGDI.NewProc("DeleteObject").Call(font)
			}
		}
	}()
	makeChild := func(class, text string, style, id uintptr) uintptr {
		child := desktopCall("CreateWindowExW", 0,
			uintptr(unsafe.Pointer(desktopString(class))),
			uintptr(unsafe.Pointer(desktopString(text))),
			style, 0, 0, 320, 32, parent, id, 0, 0)
		if child == 0 {
			t.Fatalf("无法创建原生子控件：%s", class)
		}
		return child
	}
	const childVisible uintptr = 0x50000000
	view.status = makeChild("STATIC", "初始状态", childVisible, 11)
	view.output = makeChild("EDIT", "", childVisible|0x0080|0x0004, 12)
	view.upstreamHost = makeChild("EDIT", "", childVisible|0x0080, 107)
	view.upstreamPort = makeChild("EDIT", "", childVisible|0x0080, 108)
	view.saveSettings = makeChild("BUTTON", "保存配置", childVisible|0x0001, 109)
	for i := range view.buttons {
		view.buttons[i] = makeChild("BUTTON", "操作", childVisible|0x0001, uintptr(101+i))
	}
	view.copyOutput = makeChild("BUTTON", "复制全部", childVisible|0x0001, 111)
	view.detailButton = makeChild("BUTTON", "查看详细记录", childVisible|0x0001, 112)
	view.copyStatus = makeChild("BUTTON", "复制状态", childVisible|0x0001, 116)
	desktopCurrent = view

	view.openSettings()
	if got := desktopGetText(view.upstreamHost); got != "127.0.0.1" {
		t.Fatalf("读取配置后的主机为 %q", got)
	}
	if got := desktopGetText(view.upstreamPort); got != "1080" {
		t.Fatalf("读取配置后的端口为 %q", got)
	}
	if view.dirty || desktopCall("IsWindowEnabled", view.saveSettings) != 0 {
		t.Fatal("载入配置后保存按钮应禁用")
	}

	setNativeText(t, view.upstreamHost, "proxy.example.test")
	setNativeText(t, view.upstreamPort, "2080")
	view.settingsChanged()
	if !view.dirty || desktopCall("IsWindowEnabled", view.saveSettings) == 0 {
		t.Fatal("修改上游地址后保存按钮应启用")
	}

	release, err := lockBackground()
	if err != nil {
		t.Logf("后台互斥锁正被使用，跳过会写入配置的断言：%v", err)
	} else {
		release()
		view.saveSettingsValues()
		saved, err := config.Load(path)
		if err != nil {
			t.Fatalf("读取保存后的配置失败：%v", err)
		}
		if saved.Upstream.Host != "proxy.example.test" || saved.Upstream.Port != 2080 {
			t.Fatalf("保存后的上游地址不符：%s:%d", saved.Upstream.Host, saved.Upstream.Port)
		}
		if view.dirty || desktopCall("IsWindowEnabled", view.saveSettings) != 0 {
			t.Fatal("保存成功后应清除未保存状态并禁用保存按钮")
		}

		beforeInvalid, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取保存前配置失败：%v", err)
		}
		setNativeText(t, view.upstreamHost, "changed.example.test")
		setNativeText(t, view.upstreamPort, "不是端口")
		view.settingsChanged()
		view.saveSettingsValues()
		afterInvalid, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取非法端口后的配置失败：%v", err)
		}
		if !bytes.Equal(afterInvalid, beforeInvalid) {
			t.Fatal("非法端口不应修改配置文件")
		}
		if view.settingsError != view.upstreamPort || view.settingsNotice == "" || !view.dirty {
			t.Fatal("非法端口应在端口输入框显示提示并保留未保存状态")
		}

		setNativeText(t, view.upstreamHost, "blocked.example.test")
		setNativeText(t, view.upstreamPort, "3080")
		view.settingsChanged()
		view.panel.Running = true
		view.saveSettingsValues()
		afterRunning, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取后台运行期间的配置失败：%v", err)
		}
		if !bytes.Equal(afterRunning, beforeInvalid) {
			t.Fatal("后台运行期间不应修改配置文件")
		}
		if view.settingsNotice == "" || !view.dirty {
			t.Fatal("后台运行期间应阻止保存并保留未保存状态")
		}
	}

	view.panel.Title = "定时刷新后的状态"
	view.outputText = "第一行\r\n第二行"
	setNativeText(t, view.output, view.outputText)
	desktopCall("SendMessageW", view.output, 0xB1, 2, 7)
	setNativeText(t, view.upstreamHost, "typed.example.test")
	setNativeText(t, view.upstreamPort, "4096")
	desktopCall("SendMessageW", view.upstreamHost, 0xB1, 3, 8)
	view.updateControls()
	view.updateControls()
	if got := desktopGetText(view.output); got != view.outputText {
		t.Fatalf("状态刷新更改了诊断文本：%q", got)
	}
	if got := desktopGetText(view.upstreamHost); got != "typed.example.test" {
		t.Fatalf("状态刷新更改了主机输入：%q", got)
	}
	if got := desktopGetText(view.upstreamPort); got != "4096" {
		t.Fatalf("状态刷新更改了端口输入：%q", got)
	}
	assertNativeSelection(t, view.output, 2, 7)
	assertNativeSelection(t, view.upstreamHost, 3, 8)
}

func TestDesktopStartupConfigUsesExecutableDirectory(t *testing.T) {
	executableDir := filepath.Join(t.TempDir(), "安装目录")
	if err := os.MkdirAll(executableDir, 0o755); err != nil {
		t.Fatalf("创建临时程序目录失败：%v", err)
	}
	executable := filepath.Join(executableDir, "路由助手.exe")
	path, err := prepareDesktopStartupForExecutable(executable)
	if err != nil {
		t.Fatalf("准备默认启动配置失败：%v", err)
	}
	wantPath := filepath.Join(executableDir, "config.yaml")
	if path != wantPath {
		t.Fatalf("配置路径为 %q，预期程序目录中的 %q", path, wantPath)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("未创建可用的默认配置：%v", err)
	}
	if err := config.UpdateUpstream(path, "kept.example.test", 2081); err != nil {
		t.Fatalf("准备已有配置样本失败：%v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取已有配置失败：%v", err)
	}
	if _, err := prepareDesktopStartupForExecutable(executable); err != nil {
		t.Fatalf("再次准备启动配置失败：%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("再次读取已有配置失败：%v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("重复启动配置准备不应覆盖已有配置")
	}
}

func setNativeText(t *testing.T, window uintptr, text string) {
	t.Helper()
	if desktopCall("SetWindowTextW", window, uintptr(unsafe.Pointer(desktopString(text)))) == 0 {
		t.Fatalf("设置原生控件文本失败：%s", text)
	}
}

func assertNativeSelection(t *testing.T, window uintptr, wantStart, wantEnd uint32) {
	t.Helper()
	var start, end uint32
	desktopCall("SendMessageW", window, 0xB0, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	if start != wantStart || end != wantEnd {
		t.Errorf("原生选区为 %d-%d，预期 %d-%d", start, end, wantStart, wantEnd)
	}
}
