//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"

	"codex-router/config"
	"codex-router/diagnosis"
)

type desktopDiagnosisReport struct {
	Summary string
	Detail  string
	Steps   []diagnosis.Step
	Failed  bool
}

func runDesktopDiagnosis(path string) desktopDiagnosisReport {
	resolved, pathErr := chooseConfigPath(path)
	if pathErr != nil {
		steps := diagnosis.Check(context.Background(), nil).Steps
		steps[0].Detail = "配置路径无效，无法执行上游检测：" + pathErr.Error()
		steps[0].Advice = "检查并选择有效的配置文件路径"
		steps = append(steps, routingSteps(path)...)
		return makeDesktopDiagnosisReport(steps, "")
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		steps := diagnosis.Check(context.Background(), nil).Steps
		steps[0].Detail = "配置无效，无法执行上游检测"
		steps[0].Advice = "检查配置文件是否存在、格式、监听地址、上游端口及检测地址"
		steps = append(steps, routingSteps(resolved)...)
		return makeDesktopDiagnosisReport(steps, "")
	}

	notice := diagnosisConfigurationNotice(resolved)
	report := diagnosis.Check(context.Background(), cfg)
	report.Steps = append(report.Steps, routingSteps(resolved)...)
	return makeDesktopDiagnosisReport(report.Steps, notice)
}

func makeDesktopDiagnosisReport(steps []diagnosis.Step, notice string) desktopDiagnosisReport {
	report := desktopDiagnosisReport{Steps: append([]diagnosis.Step(nil), steps...)}
	passed, failed, skipped := 0, 0, 0
	for _, step := range report.Steps {
		switch step.Status {
		case "通过":
			passed++
		case "失败":
			failed++
			report.Failed = true
		case "跳过":
			skipped++
		}
	}
	parts := []string{fmt.Sprintf("%d项检查", len(report.Steps)), fmt.Sprintf("%d项通过", passed)}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d项失败", failed))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d项跳过", skipped))
	}
	other := len(report.Steps) - passed - failed - skipped
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d项状态未知", other))
	}
	report.Summary = strings.Join(parts, "，")
	report.Detail = formatDesktopDiagnosisDetail(report.Steps, notice)
	return report
}

func formatDesktopDiagnosisDetail(steps []diagnosis.Step, notice string) string {
	var b strings.Builder
	if notice != "" {
		b.WriteString(strings.TrimSpace(notice))
		b.WriteByte('\n')
	}
	for _, step := range steps {
		fmt.Fprintf(&b, "%s：%s（%d 毫秒）\n说明：%s\n", step.Name, step.Status, step.DurationMS, step.Detail)
		advice := step.Advice
		if advice == "" {
			advice = "无"
		}
		fmt.Fprintf(&b, "建议：%s\n", advice)
	}
	b.WriteString("只有最近真实应用目标连接才能辅助确认应用正在使用代理；诊断成功不能证明所有流量已接管。\n")
	return b.String()
}
