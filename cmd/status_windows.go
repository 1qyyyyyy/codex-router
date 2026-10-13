//go:build windows

package main

import (
	"codex-router/config"
	"codex-router/diagnosis"
	"codex-router/health"
	"codex-router/integration"
	"codex-router/proxy"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type statusSnapshot struct {
	ConfigChanged bool                `json:"配置已修改"`
	Running       bool                `json:"后台运行"`
	Started       time.Time           `json:"启动时间"`
	Proxy         string              `json:"本地入口"`
	Health        health.Result       `json:"上游健康"`
	Fresh         bool                `json:"健康结果新鲜"`
	Targets       []proxy.Observation `json:"最近应用目标"`
}

func fetchControl(state backgroundState, action string, value any) error {
	req, err := http.NewRequest(http.MethodGet, "http://"+state.Address+"/"+action, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.Token)
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	if action == "snapshot" {
		client.Timeout = 2 * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法联系后台代理")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("后台拒绝状态或检测请求")
	}
	return json.NewDecoder(resp.Body).Decode(value)
}

func localProbe(cfg *config.Config, server *proxy.Server) diagnosis.Step {
	started := time.Now()
	step := diagnosis.Step{Name: "本地入口", Status: "失败", Advice: "检查本地监听端口、目标域名列表及上游代理，然后重试"}

	target, err := url.Parse(cfg.Health.URL)
	if err != nil || !cfg.MatchTarget(target.Hostname()) {
		step.Detail = "检测目标未匹配目标域名，不能验证目标路由"
		return step
	}
	local, _ := url.Parse(cfg.ProxyURL())
	headers := http.Header{}
	headers.Set(proxy.InternalProbeHeader, server.InternalProbeToken())
	tr := &http.Transport{Proxy: http.ProxyURL(local), ProxyConnectHeader: headers, TLSHandshakeTimeout: time.Duration(cfg.Health.TimeoutSeconds) * time.Second}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(min(cfg.Health.TimeoutSeconds, 10))*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Health.URL, nil)
	if err == nil {
		if target.Scheme == "http" {
			req.Header.Set(proxy.InternalProbeHeader, server.InternalProbeToken())
		}
		client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 500 || resp.StatusCode == 407 {
				err = fmt.Errorf("响应异常")
			}
		}
	}
	step.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		step.Detail = "本地入口到检测目标的链路失败"
		return step
	}
	step.Status = "通过"
	step.Detail = "本地入口到目标链路通过；检测流量不计入应用目标连接"
	step.Advice = ""
	return step
}

func routingSteps(path string) []diagnosis.Step {
	local := diagnosis.Step{Name: "本地入口", Status: "跳过", Detail: "后台入口未启动，尚不能确认应用接管", Advice: "先启动后台代理，完全退出并重新打开应用"}
	system := diagnosis.Step{Name: "系统代理", Status: "跳过", Detail: "后台未运行，未确认系统代理接管", Advice: "启动后台代理后重试"}
	statePath, _ := backgroundPaths(path)
	state, err := readBackground(statePath)
	if err != nil || contactBackground(state, "status") != nil {
		cfg, loadErr := config.Load(path)
		if loadErr == nil {
			listener, listenErr := net.Listen("tcp", cfg.ListenAddress())
			if listenErr != nil {
				local.Status = "失败"
				local.Detail = "后台未运行，但本地端口无法绑定，可能被其他程序占用"
				local.Advice = "关闭占用本地端口的程序或修改监听端口，再启动后台"
			} else {
				listener.Close()
				local.Detail = "后台入口未启动；监听端口可用，未确认应用接管"
			}
			_, backupPath := backgroundPaths(path)
			if _, statErr := os.Stat(backupPath); statErr == nil {
				system.Status = "失败"
				system.Detail = "后台未运行，但存在未恢复的代理备份"
				system.Advice = "先执行停止代理恢复原设置，再重新启动"
			} else if integration.VerifySystemProxy(cfg.ProxyURL()) == nil {
				system.Status = "失败"
				system.Detail = "系统代理仍指向本工具，但后台入口未启动"
				system.Advice = "执行停止代理恢复原设置，再重新启动"
			}
		}
		return []diagnosis.Step{local, system}
	}
	local.Status = "失败"
	if err := fetchControl(state, "probe", &local); err != nil {
		local.Detail = "无法完成后台内部链路检测"
		local.Advice = "检查后台日志并重新启动代理"
	}
	started := time.Now()
	system.Status = "通过"
	system.Detail = "系统代理及用户代理环境变量符合后台实际入口"
	system.Advice = ""
	if err := integration.VerifySystemProxy(state.Proxy); err != nil {
		system.Status = "失败"
		system.Detail = err.Error()
		system.Advice = "停止后台以恢复原设置，再重新启动；完全退出并重新打开应用"
	}
	system.DurationMS = time.Since(started).Milliseconds()
	return []diagnosis.Step{local, system}
}

func desktopStatus(path string) string {
	path, err := chooseConfigPath(path)
	if err != nil {
		return "配置路径无效：" + err.Error()
	}
	statePath, _ := backgroundPaths(path)
	state, err := readBackground(statePath)
	if err != nil {
		return "后台：未运行\n上游健康：未检测（旧结果不作为当前状态）\n最近应用目标：暂无后台记录" + pendingRestoreNotice(path)
	}
	var snap statusSnapshot
	if fetchControl(state, "snapshot", &snap) != nil {
		return "后台：未运行或正在停止\n上游健康：未知（旧结果不作为当前状态）\n最近应用目标：暂无当前记录" + pendingRestoreNotice(path)
	}
	if !snap.Running {
		return "后台：正在停止；上游健康：未知，未确认当前应用接管"
	}
	var b strings.Builder
	current := snap.Health.Status
	if !snap.Fresh {
		current = "未知（检测结果已过期）"
	}
	fmt.Fprintf(&b, "后台：运行中\n本地入口：%s\n上游健康：%s\n健康检测时间：%s\n", snap.Proxy, current, snap.Health.Time.Format("2006-01-02 15:04:05"))
	if snap.ConfigChanged {
		b.WriteString("配置已修改：当前后台仍使用启动时配置，请停止后重新启动\n")
	}
	if !snap.Fresh {
		fmt.Fprintf(&b, "历史检测结果：%s（已过期），请执行诊断\n", snap.Health.Status)
	}
	if len(snap.Targets) == 0 {
		b.WriteString("最近应用目标：尚未观察到真实目标连接；检测通过不代表应用已接管\n")
	}
	for i := len(snap.Targets) - 1; i >= 0; i-- {
		item := snap.Targets[i]
		result := "连接失败"
		if item.Success {
			result = "连接成功"
		}
		fmt.Fprintf(&b, "应用目标：%s %s:%s 节点：%s %s\n", item.Time.Format("15:04:05"), item.Host, item.Port, item.Node, result)
	}
	return b.String()
}

func diagnosisConfigurationNotice(path string) string {
	statePath, _ := backgroundPaths(path)
	state, err := readBackground(statePath)
	if err != nil {
		return ""
	}
	var snap statusSnapshot
	if fetchControl(state, "snapshot", &snap) == nil && snap.ConfigChanged {
		return "配置已修改：上游四步检测当前配置，本地入口及系统代理检查后台启动配置；请停止后重新启动再比较结果。\n"
	}
	return ""
}

func pendingRestoreNotice(path string) string {
	_, backupPath := backgroundPaths(path)
	if _, err := os.Stat(backupPath); err == nil {
		return "\n发现遗留代理备份：请执行停止代理以恢复原系统设置。"
	}
	return ""
}
