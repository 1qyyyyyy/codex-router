//go:build windows

package main

import (
	"codex-router/config"
	"codex-router/diagnosis"
	"codex-router/health"
	"codex-router/proxy"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestDesktopConfigNoticeUsesSafeStructuredMessage(t *testing.T) {
	testCases := []struct {
		name        string
		configError *config.ConfigError
		want        string
		secret      string
	}{
		{"语法提示", &config.ConfigError{Category: config.ErrorSyntax, Field: "health.url", Line: 12, Message: "第 12 行的 health.url 配置有误。请检查引号。"}, "第 12 行", "PASSWORD_SENTINEL"},
		{"字段校验提示", &config.ConfigError{Category: config.ErrorValidation, Field: "upstream.host", Message: "配置字段 upstream.host 不符合要求。请检查主机名。"}, "upstream.host", "QUERY_SENTINEL"},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			got := desktopConfigNotice(test.configError)
			if !strings.Contains(got, test.want) {
				t.Fatalf("安全提示缺少关键信息：%q", got)
			}
			if strings.Contains(got, test.secret) {
				t.Fatalf("安全提示泄漏了配置值：%q", got)
			}
		})
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
		{"启动", 101, 0, desktopAccent},
		{"停止", 102, 0, desktopSurface},
		{"检测", 103, 0, desktopSurface},
		{"设置", 104, 0, desktopSurface},
		{"按下", 101, 1, rgb(42, 71, 114)},
		{"禁用", 101, 4, rgb(36, 41, 49)},
	} {
		view.drawButton(&desktopDrawItem{CtlID: item.id, ItemState: item.state, hDC: dc, rcItem: desktopRect{0, 0, 142, 42}})
		pixel, _, _ := desktopGDI.NewProc("GetPixel").Call(dc, 8, 8)
		if pixel != item.color {
			t.Errorf("%s按钮底色不符：实际%x，预期%x", item.name, pixel, item.color)
		}
	}
}

func TestDesktopScale(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int
		dpi   uint32
		want  int
	}{
		{"基准尺寸", 96, 96, 96},
		{"放大尺寸", 96, 144, 144},
		{"缩小尺寸", 96, 120, 120},
		{"半像素四舍五入", 1, 144, 2},
		{"负坐标", -96, 144, -144},
		{"零 DPI 使用基准", 20, 0, 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := desktopScale(test.value, test.dpi); got != test.want {
				t.Errorf("缩放结果为 %d，预期 %d", got, test.want)
			}
		})
	}
}

func TestPanelStateKeepsHealthFreshnessAndRealTargetSeparate(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	snap := statusSnapshot{
		Running: true,
		Proxy:   "http://127.0.0.1:7788",
		Fresh:   false,
		Health:  health.Result{Time: now.Add(-time.Hour), Status: "健康", Node: "节点甲"},
	}
	panel := panelStateFromSnapshot(snap, now, false)
	if !panel.Running || panel.Health != "结果已过期" || !strings.Contains(panel.HealthDetail, "历史：健康") {
		t.Fatalf("过期健康不能显示为当前成功：%+v", panel)
	}
	if panel.Target != "尚无真实连接" || !strings.Contains(panel.TargetDetail, "不代表应用已接管") {
		t.Fatalf("没有真实应用连接时不能显示已接管：%+v", panel)
	}
}

func TestPanelStateUsesLatestRealTargetAndReportsResult(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	snap := statusSnapshot{
		Running: true,
		Fresh:   true,
		Health:  health.Result{Time: now.Add(-time.Minute), Status: "健康"},
		Targets: []proxy.Observation{
			{Time: now.Add(-time.Minute), Host: "old.example", Port: "443", Node: "节点旧", Success: true},
			{Time: now.Add(-time.Second), Host: "new.example", Port: "8443", Node: "节点新", Success: false},
		},
	}
	panel := panelStateFromSnapshot(snap, now, false)
	if panel.Target != "new.example:8443" || !strings.Contains(panel.TargetDetail, "连接失败") || !strings.Contains(panel.TargetDetail, "11:59:59") || !strings.Contains(panel.TargetDetail, "节点新") {
		t.Fatalf("应显示最近真实连接及其时间和结果：%+v", panel)
	}
	if strings.Contains(panel.Subtitle, "等待") {
		t.Fatalf("已有真实目标后不应显示仍在等待：%+v", panel)
	}
}

func TestPanelStateHealthTimeCases(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	for _, item := range []struct {
		name   string
		result health.Result
		fresh  bool
		label  string
		detail string
	}{
		{name: "无时间", result: health.Result{Status: "健康"}, fresh: true, label: "尚未检测", detail: "尚未检测"},
		{name: "未来时间", result: health.Result{Time: now.Add(time.Minute), Status: "健康"}, fresh: true, label: "检测时间异常", detail: "检测时间异常"},
		{name: "新鲜结果", result: health.Result{Time: now.Add(-time.Second), Status: "健康", LatencyMS: 25}, fresh: true, label: "健康", detail: "11:59:59 · 25 毫秒"},
		{name: "错误优先", result: health.Result{Time: now.Add(-time.Second), Status: "异常", LatencyMS: 25, Error: "上游连接超时"}, fresh: true, label: "异常", detail: "11:59:59 · 上游连接超时"},
	} {
		t.Run(item.name, func(t *testing.T) {
			panel := panelStateFromSnapshot(statusSnapshot{Running: true, Fresh: item.fresh, Health: item.result}, now, false)
			if panel.Health != item.label || !strings.Contains(panel.HealthDetail, item.detail) {
				t.Fatalf("健康时间状态不符：%+v", panel)
			}
		})
	}
}

func TestPanelStateReportsStoppingAndPendingRestore(t *testing.T) {
	now := time.Now()
	panel := panelStateFromSnapshot(statusSnapshot{Running: false, Health: health.Result{Status: "健康"}}, now, true)
	if panel.Running || !panel.RestorePending || panel.Title != "正在停止" || panel.Health != "未知" {
		t.Fatalf("正在停止时状态不准确：%+v", panel)
	}
	running := panelStateFromSnapshot(statusSnapshot{Running: true}, now, true)
	if running.RestorePending {
		t.Fatalf("运行期间的正常代理备份不应标为待恢复：%+v", running)
	}
}

func TestReadDesktopPanelStateDistinguishesNotRunningAndUnreadable(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	panel := readDesktopPanelState(configPath)
	if panel.Running || panel.Title != "后台未运行" {
		t.Fatalf("缺少状态文件应显示后台未运行：%+v", panel)
	}
	statePath, _ := backgroundPaths(configPath)
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("无效状态"), 0600); err != nil {
		t.Fatal(err)
	}
	panel = readDesktopPanelState(configPath)
	if panel.Title != "后台状态读取失败" {
		t.Fatalf("损坏状态文件应显示读取失败：%+v", panel)
	}
}

func TestReadDesktopPanelStateKeepsUnreachableBackupRecoverable(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	statePath, backupPath := backgroundPaths(configPath)
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		t.Fatal(err)
	}
	state := backgroundState{PID: 123, Address: "127.0.0.1:1", Token: strings.Repeat("a", 64)}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("备份"), 0600); err != nil {
		t.Fatal(err)
	}
	panel := readDesktopPanelState(configPath)
	if panel.Title != "后台状态读取失败" || !panel.RestorePending {
		t.Fatalf("联系失败时仍应提示备份可恢复：%+v", panel)
	}
}

func TestDesktopDiagnosisReportCountsFailureAndSkipped(t *testing.T) {
	steps := []diagnosis.Step{
		{Name: "上游端口", Status: "通过"},
		{Name: "协议", Status: "通过"},
		{Name: "认证", Status: "失败", Detail: "代理拒绝认证", Advice: "核对认证设置", DurationMS: 12},
		{Name: "目标连接", Status: "跳过", Detail: "前置步骤尚未通过"},
		{Name: "本地入口", Status: "跳过", Detail: "后台入口未启动"},
		{Name: "系统代理", Status: "跳过", Detail: "后台未运行", Advice: "启动后台代理"},
	}

	report := makeDesktopDiagnosisReport(steps, "配置已修改：结果可能来自不同配置。")
	if !report.Failed {
		t.Fatal("存在失败步骤时报告应标记失败")
	}
	if report.Summary != "6项检查，2项通过，1项失败，3项跳过" {
		t.Fatalf("摘要计数错误：%s", report.Summary)
	}
	for _, want := range []string{"配置已修改：结果可能来自不同配置。", "认证：失败（12 毫秒）", "说明：代理拒绝认证", "建议：核对认证设置", "目标连接：跳过", "系统代理：跳过"} {
		if !strings.Contains(report.Detail, want) {
			t.Errorf("详情缺少 %q：%s", want, report.Detail)
		}
	}
}

func TestDesktopDiagnosisReportDoesNotTreatSkippedAsSuccess(t *testing.T) {
	report := makeDesktopDiagnosisReport([]diagnosis.Step{
		{Name: "上游端口", Status: "通过"},
		{Name: "协议", Status: "跳过"},
	}, "")
	if report.Failed {
		t.Fatal("没有失败步骤时不应标记失败")
	}
	if report.Summary != "2项检查，1项通过，1项跳过" {
		t.Fatalf("跳过步骤不能计为通过：%s", report.Summary)
	}
}

func TestDesktopDiagnosisReportAllPassed(t *testing.T) {
	report := makeDesktopDiagnosisReport([]diagnosis.Step{
		{Name: "上游端口", Status: "通过"},
		{Name: "协议", Status: "通过"},
	}, "")
	if report.Failed || report.Summary != "2项检查，2项通过" {
		t.Fatalf("全通过计数错误：失败=%v，摘要=%s", report.Failed, report.Summary)
	}
}

func TestDesktopMakeLayoutFitsCardsAcrossDPI(t *testing.T) {
	sizes := []struct {
		width, height int
	}{
		{width: 680, height: 620},
		{width: 780, height: 660},
		{width: 920, height: 660},
	}
	dpis := []uint32{96, 144, 192}

	for _, size := range sizes {
		for _, dpi := range dpis {
			t.Run(layoutCaseName(size.width, size.height, dpi), func(t *testing.T) {
				layout := desktopMakeLayout(desktopScale(size.width, dpi), desktopScale(size.height, dpi), dpi)
				toLogical := func(value int32) int32 {
					return int32((int(value)*96 + int(dpi)/2) / int(dpi))
				}
				logicalRect := func(r desktopRect) desktopRect {
					return desktopRect{Left: toLogical(r.Left), Top: toLogical(r.Top), Right: toLogical(r.Right), Bottom: toLogical(r.Bottom)}
				}
				layout.status = logicalRect(layout.status)
				layout.settings = logicalRect(layout.settings)
				layout.diagnosis = logicalRect(layout.diagnosis)
				layout.host = logicalRect(layout.host)
				layout.port = logicalRect(layout.port)
				layout.save = logicalRect(layout.save)
				layout.reload = logicalRect(layout.reload)
				layout.output = logicalRect(layout.output)
				layout.copy = logicalRect(layout.copy)
				layout.detail = logicalRect(layout.detail)
				layout.main = logicalRect(layout.main)
				layout.diagnose = logicalRect(layout.diagnose)
				layout.copyState = logicalRect(layout.copyState)

				for _, item := range []struct {
					name string
					card desktopRect
					item desktopRect
				}{
					{name: "主状态按钮", card: layout.status, item: layout.main},
					{name: "状态复制按钮", card: layout.status, item: layout.copyState},
					{name: "主机输入框", card: layout.settings, item: layout.host},
					{name: "端口输入框", card: layout.settings, item: layout.port},
					{name: "保存按钮", card: layout.settings, item: layout.save},
					{name: "重新载入按钮", card: layout.settings, item: layout.reload},
					{name: "诊断输出框", card: layout.diagnosis, item: layout.output},
					{name: "复制诊断按钮", card: layout.diagnosis, item: layout.copy},
					{name: "详情按钮", card: layout.diagnosis, item: layout.detail},
				} {
					if !desktopRectContains(item.card, item.item) {
						t.Errorf("%s 超出所属卡片：控件=%+v 卡片=%+v", item.name, item.item, item.card)
					}
				}

				for _, item := range []struct {
					name string
					rect desktopRect
				}{
					{name: "诊断按钮", rect: layout.diagnose},
					{name: "状态卡片", rect: layout.status},
					{name: "配置卡片", rect: layout.settings},
					{name: "诊断卡片", rect: layout.diagnosis},
				} {
					if item.rect.Left < 0 || item.rect.Top < 0 || item.rect.Right > int32(size.width) || item.rect.Bottom > int32(size.height) {
						t.Errorf("%s 超出客户区：控件=%+v 客户区=%dx%d", item.name, item.rect, size.width, size.height)
					}
				}

				for _, pair := range []struct {
					name string
					a, b desktopRect
				}{
					{name: "主机与端口", a: layout.host, b: layout.port},
					{name: "主机与保存按钮", a: layout.host, b: layout.save},
					{name: "端口与保存按钮", a: layout.port, b: layout.save},
				} {
					if desktopRectsOverlap(pair.a, pair.b) {
						t.Errorf("%s发生重叠：%+v 与 %+v", pair.name, pair.a, pair.b)
					}
				}

				if height := layout.output.Bottom - layout.output.Top; height < 64 {
					t.Errorf("诊断输出区高度为%d逻辑像素，小于64", height)
				}

				title := desktopRect{Left: 44, Top: layout.diagnosis.Top + 15, Right: 194, Bottom: layout.diagnosis.Top + 38}
				if desktopRectsOverlap(title, layout.copy) || desktopRectsOverlap(title, layout.detail) {
					t.Errorf("复制或详情按钮与诊断标题重叠：标题=%+v 复制=%+v 详情=%+v", title, layout.copy, layout.detail)
				}
			})
		}
	}
}

func TestDesktopValidateFields(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		portText string
		wantPort int
		wantID   uintptr
	}{
		{name: "正常主机名", host: " proxy.example.test ", portText: "8080", wantPort: 8080},
		{name: "IPv4", host: "192.0.2.10", portText: "3128", wantPort: 3128},
		{name: "IPv6", host: "2001:db8::10", portText: "1080", wantPort: 1080},
		{name: "空主机", host: "", portText: "8080", wantID: 107},
		{name: "端口下界外", host: "proxy.example.test", portText: "0", wantID: 108},
		{name: "端口上界外", host: "proxy.example.test", portText: "65536", wantID: 108},
		{name: "包含协议", host: "http://proxy.example.test", portText: "8080", wantID: 107},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port, id, _ := desktopValidateFields(tc.host, tc.portText)
			if port != tc.wantPort || id != tc.wantID {
				t.Errorf("校验结果=(端口%d,控件%v)，期望=(端口%d,控件%v)", port, id, tc.wantPort, tc.wantID)
			}
		})
	}
}

func TestDesktopCompactDiagnosisIncludesFailedAdvice(t *testing.T) {
	report := desktopDiagnosisReport{Steps: []diagnosis.Step{
		{Name: "认证", Status: "失败", DurationMS: 23, Advice: "核对上游用户名和密码"},
	}}
	compact := desktopCompactDiagnosis(report)
	for _, want := range []string{"失败  认证", "23 毫秒", "建议：核对上游用户名和密码"} {
		if !strings.Contains(compact, want) {
			t.Errorf("紧凑诊断缺少%q：%s", want, compact)
		}
	}
}

func desktopRectContains(outer, inner desktopRect) bool {
	return inner.Left >= outer.Left && inner.Top >= outer.Top && inner.Right <= outer.Right && inner.Bottom <= outer.Bottom
}

func desktopRectsOverlap(a, b desktopRect) bool {
	return a.Left < b.Right && b.Left < a.Right && a.Top < b.Bottom && b.Top < a.Bottom
}

func layoutCaseName(width, height int, dpi uint32) string {
	return fmt.Sprintf("窗口%dx%d_缩放%d", width, height, dpi)
}
