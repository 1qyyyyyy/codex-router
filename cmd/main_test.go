package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLaunchArgsSeparatesApplicationArguments(t *testing.T) {
	path, app, appArgs, err := parseLaunchArgs([]string{"--config", "router.yaml", "--app", "client.exe", "--", "--profile", "测试档案"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "router.yaml" || app != "client.exe" || strings.Join(appArgs, "|") != "--profile|测试档案" {
		t.Fatalf("launch 参数解析错误：配置=%q 应用=%q 参数=%q", path, app, appArgs)
	}
}

func TestParseLaunchRequiresConfigAndDelimiterForAppArgs(t *testing.T) {
	if _, _, _, err := parseLaunchArgs([]string{"--app", "client.exe"}); err == nil {
		t.Fatal("launch 应要求配置路径")
	}
	if _, _, _, err := parseLaunchArgs([]string{"--config", "router.yaml", "--app", "client.exe", "--profile"}); err == nil {
		t.Fatal("应用参数应要求放在 -- 之后")
	}
}

func TestHelpFlagsAreRecognizedOnlyBeforeApplicationDelimiter(t *testing.T) {
	if !hasHelpFlag([]string{"--config", "router.yaml", "--help"}) {
		t.Fatal("应识别命令帮助选项")
	}
	if hasHelpFlag([]string{"--config", "router.yaml", "--", "--help"}) {
		t.Fatal("不能把应用参数当作命令帮助选项")
	}
}

func TestParseLogLevelUsesConfiguredSeverity(t *testing.T) {
	for name, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	} {
		got, err := parseLogLevel(name)
		if err != nil || got != want {
			t.Errorf("日志级别 %s 解析错误：%v，%v", name, got, err)
		}
	}
	if _, err := parseLogLevel("verbose"); err == nil {
		t.Fatal("应拒绝不支持的日志级别")
	}
}

func TestLoggerHonorsConfiguredLevelAndChineseFields(t *testing.T) {
	console := &bytes.Buffer{}
	path := filepath.Join(t.TempDir(), "router.log")
	logger, closeLog, err := newLoggerWithOutput(path, "warn", console)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("不应输出的信息")
	logger.Warn("应输出的警告")
	closeLog()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{console.String(), string(file)} {
		if strings.Contains(output, "不应输出的信息") || !strings.Contains(output, "应输出的警告") || !strings.Contains(output, "级别=警告") || !strings.Contains(output, "时间=") {
			t.Fatalf("日志级别或中文字段配置未生效：%s", output)
		}
	}
}

func TestRestoreUsesBackupWithoutLoadingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "不存在的配置.yaml")
	err := systemProxy("restore", path)
	if err == nil || !strings.Contains(err.Error(), "读取代理设置备份失败") {
		t.Fatalf("恢复代理应直接检查备份，不应加载配置文件：%v", err)
	}
}
