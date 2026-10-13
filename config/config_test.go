package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAndMatchTarget(t *testing.T) {
	cfg := Default()
	if cfg.Upstream.Type != "auto" || cfg.ListenAddress() != "127.0.0.1:17890" {
		t.Fatalf("默认配置不符合约定：%+v", cfg)
	}
	if cfg.ProxyURL() != "http://127.0.0.1:17890" {
		t.Fatalf("代理地址应指向本地入口：%s", cfg.ProxyURL())
	}
	for _, host := range []string{"api.openai.com", "chatgpt.com", "auth.openai.com"} {
		if !cfg.MatchTarget(host) {
			t.Errorf("默认目标未匹配：%s", host)
		}
	}
	cfg.Targets = []string{"example.com", "*.sub.example.org"}
	for _, host := range []string{"example.com", "a.sub.example.org", "A.SUB.EXAMPLE.ORG."} {
		if !cfg.MatchTarget(host) {
			t.Errorf("应匹配目标：%s", host)
		}
	}
	for _, host := range []string{"www.example.com", "sub.example.org", "badexample.com", "example.com.evil"} {
		if cfg.MatchTarget(host) {
			t.Errorf("不应匹配目标：%s", host)
		}
	}
}

func TestWriteDefaultDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("用户已有内容\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) == string(original) || string(after) != "用户已有内容\n" {
		t.Fatalf("默认配置覆盖了已有文件：%q", after)
	}
}

func TestLoadStrictEnvAndRelativePaths(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	content := strings.ReplaceAll(`server:
  host: 127.0.0.1
  port: 17890
upstream:
  type: socks5
  host: 127.0.0.1
  port: 1080
  password: ${ROUTER_TEST_SECRET}
targets: [api.openai.com]
log:
  level: info
  file: logs/router.log
health:
  url: https://api.openai.com/
  interval_seconds: 30
  timeout_seconds: 5
  state_file: state/health.json
launcher:
  executable: apps/client.exe
  args: []
`, `\n`, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_TEST_SECRET", "保密值")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream.Password != "保密值" {
		t.Fatalf("环境变量密码未展开：%q", cfg.Upstream.Password)
	}
	if cfg.Log.File != filepath.Join(base, "logs", "router.log") || cfg.Health.StateFile != filepath.Join(base, "state", "health.json") || cfg.Launcher.Executable != filepath.Join(base, "apps", "client.exe") {
		t.Fatalf("相对路径未按配置目录解析：%+v", cfg)
	}
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("未知配置键未被拒绝")
	}
}

func TestLoadMissingPasswordEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	text := `server: {host: 127.0.0.1, port: 17890}
upstream: {type: auto, host: 127.0.0.1, port: 1080, password: ${ROUTER_MISSING_SECRET}}
targets: [api.openai.com]
log: {level: info}
health: {url: https://api.openai.com/, interval_seconds: 30, timeout_seconds: 5}
`
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(text, `\n`, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_MISSING_SECRET", "")
	if err := os.Unsetenv("ROUTER_MISSING_SECRET"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("未定义的密码环境变量应报错")
	}
}

func TestValidateRejectsNonLoopbackAndBadType(t *testing.T) {
	cfg := Default()
	cfg.Server.Host = "0.0.0.0"
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝非环回监听地址")
	}
	cfg = Default()
	cfg.Upstream.Type = "unknown"
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝未知上游类型")
	}
	cfg = Default()
	cfg.Targets = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝空目标列表")
	}
	cfg = Default()
	cfg.Upstream.Host = "bad..host"
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝不合法的上游主机名")
	}
	cfg = Default()
	cfg.Upstream.Host = "localhost"
	cfg.Upstream.Port = cfg.Server.Port
	if err := cfg.Validate(); err == nil {
		t.Fatal("应拒绝与本地环回监听同端口的上游地址")
	}
	cfg = Default()
	cfg.Health.URL = "https://user:password@example.com/check"
	if err := cfg.Validate(); err == nil {
		t.Fatal("健康检测地址不应允许用户名或密码")
	}
}

func TestValidateRejectsHealthDurationOverflow(t *testing.T) {
	cfg := Default()
	cfg.Health.IntervalSeconds = MaxHealthSeconds + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("过大的健康检测间隔应被拒绝")
	}
	cfg = Default()
	cfg.Health.TimeoutSeconds = MaxHealthSeconds + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("过大的健康检测超时应被拒绝")
	}
}
