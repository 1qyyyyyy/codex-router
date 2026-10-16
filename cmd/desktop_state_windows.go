//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type desktopPanelState struct {
	Running        bool
	RestorePending bool
	Title          string
	Subtitle       string
	Entry          string
	Health         string
	HealthDetail   string
	Target         string
	TargetDetail   string
	ConfigChanged  bool
	Updated        time.Time
}

func readDesktopPanelState(path string) desktopPanelState {
	statePath, backupPath := backgroundPaths(path)
	_, backupErr := os.Stat(backupPath)
	panel := desktopPanelState{RestorePending: backupErr == nil, Updated: time.Now()}

	state, err := readBackground(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			panel.Title = "后台未运行"
			panel.Subtitle = "尚未接管应用流量"
			panel.Entry = "尚未启动"
			panel.Health = "未检测"
			panel.HealthDetail = "后台未运行，历史结果不代表当前健康状态"
			panel.Target = "尚无真实连接"
			panel.TargetDetail = "健康检测不代表应用已接管"
			if panel.RestorePending {
				panel.Subtitle = "后台未运行，发现待恢复的代理备份"
			}
			return panel
		}
		panel.Title = "后台状态读取失败"
		panel.Subtitle = "无法读取后台状态文件"
		panel.Entry = "状态未知"
		panel.Health = "未知"
		panel.HealthDetail = "状态读取失败，不能确认当前健康状态"
		panel.Target = "无法读取"
		panel.TargetDetail = "状态读取失败，不能确认应用连接"
		return panel
	}

	var snap statusSnapshot
	if err := fetchControl(state, "snapshot", &snap); err != nil {
		panel.Title = "后台状态读取失败"
		panel.Subtitle = "无法读取后台实时状态"
		panel.Entry = "状态未知"
		panel.Health = "未知"
		panel.HealthDetail = "后台快照读取失败，不能确认当前健康状态"
		panel.Target = "无法读取"
		panel.TargetDetail = "后台快照读取失败，不能确认应用连接"
		return panel
	}
	return panelStateFromSnapshot(snap, time.Now(), panel.RestorePending && !snap.Running)
}

func panelStateFromSnapshot(snap statusSnapshot, now time.Time, restorePending bool) desktopPanelState {
	panel := desktopPanelState{RestorePending: restorePending && !snap.Running, Updated: now, Entry: "尚未启动"}
	if !snap.Running {
		panel.Title = "正在停止"
		panel.Subtitle = "后台正在关闭并恢复系统代理"
		panel.Entry = "停止中"
		panel.Health = "未知"
		panel.HealthDetail = "后台正在停止，不能确认当前健康状态"
		panel.Target = "未确认接管"
		panel.TargetDetail = "后台正在停止，不能确认当前应用连接"
		return panel
	}

	panel.Running = true
	panel.Title = "后台运行中"
	panel.Subtitle = "正在等待真实应用连接"
	panel.Entry = snap.Proxy
	panel.ConfigChanged = snap.ConfigChanged
	if panel.ConfigChanged {
		panel.Subtitle = "配置已修改；后台仍使用启动时配置"
	}

	panel.Health = snap.Health.Status
	if panel.Health == "" {
		panel.Health = "未知"
	}
	healthTime := snap.Health.Time
	if healthTime.IsZero() {
		panel.Health = "尚未检测"
		panel.HealthDetail = "尚未检测"
	} else if healthTime.After(now) {
		panel.Health = "检测时间异常"
		panel.HealthDetail = "检测时间异常，不能作为当前健康状态"
	} else if !snap.Fresh {
		panel.Health = "结果已过期"
		panel.HealthDetail = fmt.Sprintf("上次检测 %s · 历史：%s", healthTime.Format("15:04:05"), snap.Health.Status)
	} else {
		panel.HealthDetail = fmt.Sprintf("%s · %d 毫秒", healthTime.Format("15:04:05"), snap.Health.LatencyMS)
		if snap.Health.Error != "" {
			panel.HealthDetail = fmt.Sprintf("%s · %s", healthTime.Format("15:04:05"), compactHealthError(snap.Health.Error))
		}
	}

	if len(snap.Targets) == 0 {
		panel.Target = "尚无真实连接"
		panel.TargetDetail = "尚未观察到应用目标连接；健康检测不代表应用已接管"
		return panel
	}
	latest := snap.Targets[0]
	for _, target := range snap.Targets[1:] {
		if target.Time.After(latest.Time) {
			latest = target
		}
	}
	result := "连接失败"
	if latest.Success {
		result = "连接成功"
	}
	panel.Target = latest.Host + ":" + latest.Port
	panel.TargetDetail = fmt.Sprintf("%s %s；节点：%s", latest.Time.Format("15:04:05"), result, latest.Node)
	if !panel.ConfigChanged {
		panel.Subtitle = "已观察到应用目标连接"
	}
	return panel
}

func compactHealthError(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 30 {
		return string(runes[:30]) + "…"
	}
	return value
}
