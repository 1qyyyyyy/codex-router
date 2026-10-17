package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadReturnsSafeStructuredConfigErrors(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	valid := `server:
  host: 127.0.0.1
  port: 17890
upstream:
  type: auto
  host: 127.0.0.1
  port: 1080
  password: "PASSWORD_SENTINEL"
targets: [api.openai.com]
log: {level: info, file: logs/router.log}
health: {url: "https://example.com/check?token=QUERY_SENTINEL", interval_seconds: 30, timeout_seconds: 5, state_file: state.json}
`
	for _, test := range []struct {
		name, content string
		category      ErrorCategory
		field         string
		line          int
		advice        string
	}{
		{"语法错误", strings.Replace(valid, `password: "PASSWORD_SENTINEL"`, `password: "PASSWORD_SENTINEL`, 1), ErrorSyntax, "配置文件", 11, "引号"},
		{"未知字段", strings.Replace(valid, "  type: auto", "  PASSWORD_SENTINEL: true", 1), ErrorUnknownField, "upstream", 0, "未识别字段"},
		{"类型错误", strings.Replace(valid, "  port: 1080", "  port: PASSWORD_SENTINEL", 1), ErrorType, "upstream.port", 7, "数据类型"},
		{"类型与未知字段混合", strings.Replace(valid, "  port: 1080", "  port: PASSWORD_SENTINEL\n  PASSWORD_SENTINEL: true", 1), ErrorType, "upstream.port", 7, "数据类型"},
		{"嵌套类型错误", strings.Replace(valid, "  type: auto", "  type:\n    password: PASSWORD_SENTINEL", 1), ErrorType, "upstream.type", 6, "数据类型"},
		{"重复字段", strings.Replace(valid, "  port: 1080", "  port: 1080\n  port: 1081", 1), ErrorSyntax, "upstream.port", 8, "重复字段"},
		{"字段校验", strings.Replace(valid, "targets: [api.openai.com]", "targets: [PASSWORD_SENTINEL]", 1), ErrorValidation, "targets", 9, "域名"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("预期配置错误")
			}
			var configErr *ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("错误未结构化：%T", err)
			}
			if configErr.Category != test.category {
				t.Errorf("类别=%q，预期 %q", configErr.Category, test.category)
			}
			if !strings.Contains(configErr.Field, test.field) {
				t.Errorf("字段提示缺失：%q", configErr.Field)
			}
			if test.line != 0 && configErr.Line != test.line {
				t.Errorf("行号=%d，预期 %d", configErr.Line, test.line)
			}
			if test.category == ErrorValidation && test.line > 0 && !strings.Contains(configErr.Message, "第 9 行") {
				t.Errorf("用户提示未展示字段行号：%q", configErr.Message)
			}
			if !strings.Contains(configErr.Message, test.advice) {
				t.Errorf("缺少操作建议：%q", configErr.Message)
			}
			if strings.Contains(err.Error(), "PASSWORD_SENTINEL") || strings.Contains(err.Error(), "QUERY_SENTINEL") {
				t.Errorf("错误泄漏了配置值：%s", err)
			}
		})
	}
}

func TestLoadMissingPasswordEnvironmentShowsSafeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server: {host: 127.0.0.1, port: 17890}
upstream: {type: auto, host: 127.0.0.1, port: 1080, password: "${ROUTER_MISSING_SECRET}"}
targets: [api.openai.com]
log: {level: info, file: logs/router.log}
health: {url: https://example.com/, interval_seconds: 30, timeout_seconds: 5, state_file: state.json}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTER_MISSING_SECRET", "")
	if err := os.Unsetenv("ROUTER_MISSING_SECRET"); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("应返回结构化环境变量错误：%v", err)
	}
	if configErr.Field != "upstream.password" || configErr.Line != 2 {
		t.Fatalf("字段或行号不正确：%+v", configErr)
	}
	if !strings.Contains(configErr.Message, "第 2 行") || strings.Contains(err.Error(), "ROUTER_MISSING_SECRET") {
		t.Fatalf("安全提示未展示行号或泄漏变量名：%s", err)
	}
}

func TestLoadReadErrorPreservesIs(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("应保留文件不存在语义：%v", err)
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Category != ErrorRead {
		t.Fatalf("应返回结构化读取错误：%v", err)
	}
}

func TestUpdateUpstream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.Upstream.Name = "测试出口"
	cfg.Upstream.Type = "socks5"
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateUpstream(path, "192.168.1.20", 7890); err != nil {
		t.Fatal(err)
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Upstream.Host != "192.168.1.20" || updated.Upstream.Port != 7890 || updated.Upstream.Name != "测试出口" || updated.Upstream.Type != "socks5" {
		t.Fatalf("上游更新或其他配置保留失败：%+v", updated.Upstream)
	}
	if err := UpdateUpstream(path, "bad..host", 7890); err == nil {
		t.Fatal("非法主机名应被拒绝")
	} else {
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "upstream.host" || !strings.Contains(err.Error(), "主机名") {
			t.Fatalf("保存时非法主机应保留可定位提示：%v", err)
		}
	}
	if err := UpdateUpstream(path, "192.168.1.20", 0); err == nil {
		t.Fatal("非法端口应被拒绝")
	} else {
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "upstream.port" {
			t.Fatalf("保存时非法端口应保留字段提示：%v", err)
		}
	}
}

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
