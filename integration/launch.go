package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"codex-router/config"
)

// Launch 在子进程级别配置代理，然后等待应用退出。
func Launch(ctx context.Context, cfg *config.Config, executable string, args []string) error {
	if cfg == nil {
		return fmt.Errorf("启动应用需要有效配置")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target := strings.TrimSpace(executable)
	if target == "" {
		target = strings.TrimSpace(cfg.Launcher.Executable)
	}
	if target == "" {
		target = discoverApplication()
	}
	if target == "" {
		return fmt.Errorf("未找到桌面应用。请使用 launch --app 指定应用的完整可执行文件路径")
	}
	resolved, err := resolveApplication(target)
	if err != nil {
		return err
	}
	proxyURL := localProxyURL(cfg)
	commandArgs := append([]string(nil), cfg.Launcher.Args...)
	commandArgs = append(commandArgs, args...)
	if err := rejectProxyArguments(commandArgs); err != nil {
		return err
	}
	commandArgs = append(commandArgs, electronProxyArgs(proxyURL)...)
	cmd := exec.CommandContext(ctx, resolved, commandArgs...)
	cmd.Env = mergeEnvironment(os.Environ(), proxyEnvironment(proxyURL))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("启动应用失败：%w", err)
	}
	return nil
}

func localProxyURL(cfg *config.Config) string {
	address := cfg.ListenAddress()
	if !strings.Contains(address, ":") || strings.HasPrefix(address, ":") {
		address = "127.0.0.1:" + strings.TrimPrefix(address, ":")
	}
	return "http://" + address
}

func proxyEnvironment(proxyURL string) map[string]string {
	if runtime.GOOS == "windows" {
		return map[string]string{
			"HTTP_PROXY": proxyURL, "HTTPS_PROXY": proxyURL, "ALL_PROXY": proxyURL,
			"NO_PROXY": "localhost,127.0.0.1,::1",
		}
	}
	return map[string]string{
		"HTTP_PROXY": proxyURL, "http_proxy": proxyURL,
		"HTTPS_PROXY": proxyURL, "https_proxy": proxyURL,
		"ALL_PROXY": proxyURL, "all_proxy": proxyURL,
		"NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1",
	}
}

func electronProxyArgs(proxyURL string) []string {
	return []string{"--proxy-server=" + proxyURL, "--proxy-bypass-list=localhost;127.0.0.1;[::1]"}
}

func mergeEnvironment(current []string, updates map[string]string) []string {
	result := make([]string, 0, len(current)+len(updates))
	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	updated := make(map[string]bool, len(updates))
	for _, key := range keys {
		updated[key] = true
	}
	for _, item := range current {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			result = append(result, item)
			continue
		}
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if !updated[key] {
			result = append(result, item)
		}
	}
	for _, key := range keys {
		result = append(result, key+"="+updates[key])
	}
	return result
}

func rejectProxyArguments(args []string) error {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--proxy-server" || strings.HasPrefix(lower, "--proxy-server=") || lower == "--no-proxy-server" || strings.HasPrefix(lower, "--no-proxy-server=") || lower == "--proxy-bypass-list" || strings.HasPrefix(lower, "--proxy-bypass-list=") {
			return fmt.Errorf("应用参数 %q 与代理启动设置冲突；请从配置或命令中移除该参数", arg)
		}
	}
	return nil
}

func resolveApplication(target string) (string, error) {
	return resolvePlatformApplication(target)
}

func existingPath(path string) string {
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}
