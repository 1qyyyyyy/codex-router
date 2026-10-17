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

func TestIsDesktopInvocation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{"无参数启动", nil, true},
		{"面板命令", []string{"panel", "--config", "router.yaml"}, true},
		{"托盘命令", []string{"tray"}, true},
		{"诊断命令", []string{"diagnose"}, false},
		{"后台命令", []string{"background", "--config", "router.yaml"}, false},
		{"启动命令", []string{"start", "--config", "router.yaml"}, false},
		{"停止命令", []string{"stop", "--config", "router.yaml"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isDesktopInvocation(test.args); got != test.want {
				t.Errorf("是否桌面启动为 %t，预期 %t", got, test.want)
			}
		})
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

func TestRotatingFileKeepsSizeLimitAndOnlyConfiguredBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	writer, err := newRotatingFile(path, 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"abcde", "f", "ghij", "k"} {
		if _, err := writer.Write([]byte(data)); err != nil {
			t.Fatalf("写入日志失败：%v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, want string }{
		{path, "k"}, {path + ".1", "fghij"}, {path + ".2", "abcde"},
	} {
		data, err := os.ReadFile(test.path)
		if err != nil || string(data) != test.want {
			t.Errorf("日志文件 %s 内容为 %q，错误 %v，预期 %q", test.path, data, err, test.want)
		}
		if len(data) > 5 {
			t.Errorf("日志文件 %s 超过大小上限：%d", test.path, len(data))
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("不应保留超出数量上限的备份：%v", err)
	}
}

func TestRotatingFileTruncatesOversizedRecordToStayBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	writer, err := newRotatingFile(path, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("超长日志写入结果为 %d，错误 %v", n, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "cdef" {
		t.Fatalf("超长日志保留内容为 %q，错误 %v，预期保留末尾数据", data, err)
	}
}

func TestRotatingFileResumesWritingAfterRotationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	writer, err := newRotatingFile(path, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("abcde")); err != nil {
		t.Fatal(err)
	}
	backupDirectory := path + ".1"
	if err := os.Mkdir(backupDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDirectory, "占位"), []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("f")); err == nil {
		t.Fatal("备份路径被占用时应报告轮转失败")
	}
	if err := os.RemoveAll(backupDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("f")); err != nil {
		t.Fatalf("轮转失败后应继续写日志：%v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "f" {
		t.Fatalf("当前日志内容为 %q，错误 %v，预期 f", current, err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil || string(backup) != "abcde" {
		t.Fatalf("日志备份内容为 %q，错误 %v，预期 abcde", backup, err)
	}
}

func TestRotatingFileTrimsOversizedExistingLogsDuringUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.log")
	for file, content := range map[string]string{
		path:        "abcdefgh",
		path + ".1": "0123456789",
		path + ".2": "uvwxyz",
	} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := newRotatingFile(path, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, want string }{
		{path, ""}, {path + ".1", "efgh"}, {path + ".2", "6789"},
	} {
		data, err := os.ReadFile(test.path)
		if err != nil || string(data) != test.want {
			t.Errorf("升级后日志 %s 内容为 %q，错误 %v，预期 %q", test.path, data, err, test.want)
		}
		if len(data) > 4 {
			t.Errorf("升级后日志 %s 仍超过大小上限：%d", test.path, len(data))
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
