//go:build !windows

package main

import "codex-router/diagnosis"

func desktopStatus(path string) string {
	return "后台管理仅支持视窗系统；未确认应用接管"
}
func routingSteps(path string) []diagnosis.Step {
	return []diagnosis.Step{{Name: "本地入口", Status: "跳过", Detail: "当前平台不支持后台入口检测"}, {Name: "系统代理", Status: "跳过", Detail: "当前平台不支持系统代理检查"}}
}
func diagnosisConfigurationNotice(path string) string { return "" }
