//go:build windows

package main

import (
	"codex-router/health"
	"strings"
	"testing"
	"time"
)

func TestDesktopLatencyStates(t *testing.T) {
	now := time.Now()
	for _, item := range []struct {
		name   string
		result health.Result
		want   string
	}{
		{"等待首次测量", health.Result{}, "正在测量"},
		{"正常", health.Result{Time: now.Add(-time.Second), Status: "健康", LatencyMS: 128}, "128 毫秒"},
		{"失败耗时不能冒充延迟", health.Result{Time: now, Status: "异常", LatencyMS: 5000}, "连接异常"},
		{"超时", health.Result{Time: now, Status: "异常", Error: "延迟检测超时", LatencyMS: 5000}, "检测超时"},
		{"过期", health.Result{Time: now.Add(-health.LatencyMaxAge - time.Second), Status: "健康", LatencyMS: 10}, "结果已过期"},
		{"未来时间", health.Result{Time: now.Add(time.Second), Status: "健康", LatencyMS: 10}, "结果已过期"},
	} {
		t.Run(item.name, func(t *testing.T) {
			panel := panelStateFromSnapshot(statusSnapshot{Running: true, Latency: item.result}, now, false)
			if panel.Latency != item.want || panel.LatencyDetail == "" {
				t.Fatalf("延迟显示错误：%s，%s", panel.Latency, panel.LatencyDetail)
			}
			if strings.Contains(panel.Latency, "毫秒") && !strings.Contains(panel.LatencyDetail, item.result.Time.Format("15:04:05")) {
				t.Fatal("有效延迟应显示采样时间")
			}
		})
	}
	stopped := panelStateFromSnapshot(statusSnapshot{Latency: health.Result{Time: now, Status: "健康", LatencyMS: 10}}, now, false)
	if stopped.Latency != "未监测" {
		t.Fatal("后台停止时不得显示旧延迟")
	}
}
