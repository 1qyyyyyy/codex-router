package main

import (
	"bytes"
	"codex-router/config"
	"codex-router/diagnosis"
	"context"
	"fmt"
	"io"
)

func runDiagnosis(path string, out io.Writer) error {
	resolved, err := chooseConfigPath(path)
	if err != nil {
		return fmt.Errorf("配置路径无效，请检查路径：%w", err)
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintln(out, "配置无效：请检查配置文件是否存在、格式、监听地址、上游端口及检测地址。")
		return fmt.Errorf("加载配置失败：%w", err)
	}
	fmt.Fprint(out, diagnosisConfigurationNotice(resolved))
	report := diagnosis.Check(context.Background(), cfg)
	report.Steps = append(report.Steps, routingSteps(resolved)...)
	failed := false
	for _, step := range report.Steps {
		if step.Status == "失败" {
			failed = true
		}
		if step.Status != "通过" {
			report.Healthy = false
		}
	}
	fmt.Fprint(out, report.String())
	fmt.Fprintln(out, "只有最近真实应用目标连接才能辅助确认应用正在使用代理；诊断成功不能证明所有流量已接管。")
	if failed {
		return fmt.Errorf("诊断发现故障，请按完整报告中的建议修复后重试")
	}
	if !report.Healthy {
		fmt.Fprintln(out, "诊断未全部通过：部分步骤未执行，尚不能确认应用已接管。")
	} else {
		fmt.Fprintln(out, "诊断六步全部通过。")
	}
	return nil
}

func desktopAction(action, path string) (string, error) {
	resolved, err := chooseConfigPath(path)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if action == "diagnose" {
		err = runDiagnosis(resolved, &out)
	} else if action == "start" || action == "stop" {
		err = backgroundCommand(action, resolved, &out)
	} else {
		return "", fmt.Errorf("不支持的桌面操作")
	}
	return out.String(), err
}
