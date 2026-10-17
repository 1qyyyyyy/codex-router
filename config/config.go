// 配置包负责读取、校验和保存路由器配置。
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrorCategory 标识配置错误来源。
type ErrorCategory string

const (
	ErrorRead         ErrorCategory = "读取"
	ErrorSyntax       ErrorCategory = "语法"
	ErrorUnknownField ErrorCategory = "未知字段"
	ErrorType         ErrorCategory = "类型"
	ErrorValidation   ErrorCategory = "字段校验"
)

// ConfigError 保存可安全展示的配置错误信息，不包含解析器原始文本或字段值。
type ConfigError struct {
	Category ErrorCategory
	Field    string
	Line     int
	Message  string
	Cause    error
}

func (e *ConfigError) Error() string { return e.Message }
func (e *ConfigError) Unwrap() error { return e.Cause }

func safeError(category ErrorCategory, field string, line int, message string, cause error) error {
	return &ConfigError{Category: category, Field: field, Line: line, Message: message, Cause: cause}
}

var yamlLinePattern = regexp.MustCompile(`(?:yaml: )?line ([0-9]+):`)
var yamlUnknownFieldPattern = regexp.MustCompile(`field ([^ ]+) not found`)

func yamlConfigError(data []byte, cause error) error {
	line := 0
	if match := yamlLinePattern.FindStringSubmatch(cause.Error()); len(match) == 2 {
		fmt.Sscan(match[1], &line)
	}
	var typed *yaml.TypeError
	if errors.As(cause, &typed) {
		if len(typed.Errors) > 0 {
			first := typed.Errors[0]
			if match := yamlLinePattern.FindStringSubmatch(first); len(match) == 2 {
				fmt.Sscan(match[1], &line)
			}
			field := yamlFieldAtLine(data, line)
			if isDuplicateYAMLKey(first) {
				if field == "" {
					field = "配置文件"
				}
				return safeError(ErrorSyntax, field, line, fieldLineMessage(field, line, "请移除重复字段，每个字段只能配置一次。"), nil)
			}
			if yamlUnknownFieldPattern.MatchString(first) {
				if field == "" {
					field = "配置文件"
				}
				location := field
				if line > 0 {
					location = fmt.Sprintf("第 %d 行的 %s", line, field)
				}
				return safeError(ErrorUnknownField, field, line, location+" 下存在未识别字段。请移除该字段，或改用配置支持的字段名。", nil)
			}
			if field == "" {
				field = "对应字段"
			}
			return safeError(ErrorType, field, line, fieldLineMessage(field, line, "请检查该字段的数据类型，例如整数、文本或列表。"), nil)
		}
	}
	field := yamlFieldAtLine(data, line)
	if field == "" {
		field = "配置文件"
	}
	if isDuplicateYAMLKey(cause.Error()) {
		return safeError(ErrorSyntax, field, line, fieldLineMessage(field, line, "请移除重复字段，每个字段只能配置一次。"), nil)
	}
	return safeError(ErrorSyntax, field, line, fieldLineMessage(field, line, "请检查冒号、缩进、引号和列表格式。"), nil)
}

func isDuplicateYAMLKey(message string) bool {
	return strings.Contains(message, "already defined")
}

func fieldLineMessage(field string, line int, advice string) string {
	if line > 0 {
		return fmt.Sprintf("第 %d 行的 %s 配置有误。%s", line, field, advice)
	}
	return fmt.Sprintf("%s 配置有误。%s", field, advice)
}

var knownConfigFields = map[string]bool{
	"server": true, "server.host": true, "server.port": true,
	"upstream": true, "upstream.type": true, "upstream.host": true, "upstream.port": true, "upstream.name": true, "upstream.username": true, "upstream.password": true,
	"targets": true, "log": true, "log.level": true, "log.file": true,
	"health": true, "health.url": true, "health.interval_seconds": true, "health.timeout_seconds": true, "health.state_file": true,
	"launcher": true, "launcher.executable": true, "launcher.args": true,
}

func safeKnownFieldPath(field string) string {
	parts := strings.Split(field, ".")
	for len(parts) > 0 {
		candidate := strings.Join(parts, ".")
		if knownConfigFields[candidate] {
			return candidate
		}
		parts = parts[:len(parts)-1]
	}
	return "配置文件"
}

func yamlFieldAtLine(data []byte, line int) string {
	if line < 1 {
		return ""
	}
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return ""
	}
	var walk func(*yaml.Node, []string) string
	walk = func(node *yaml.Node, parent []string) string {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				path := append(append([]string(nil), parent...), key.Value)
				if value.Line == line || key.Line == line {
					return safeKnownFieldPath(strings.Join(path, "."))
				}
				if found := walk(value, path); found != "" {
					return found
				}
			}
		} else if node.Kind == yaml.SequenceNode {
			for _, child := range node.Content {
				if child.Line == line {
					if len(parent) > 0 {
						return safeKnownFieldPath(strings.Join(parent, "."))
					}
				}
				if found := walk(child, parent); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return walk(root.Content[0], nil)
}

func yamlLineForField(data []byte, field string) int {
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return 0
	}
	for _, alternative := range strings.Split(field, " / ") {
		parts := strings.Split(alternative, ".")
		var find func(*yaml.Node, int) int
		find = func(node *yaml.Node, index int) int {
			if index == len(parts) {
				return node.Line
			}
			if node.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(node.Content); i += 2 {
					if node.Content[i].Value == parts[index] {
						if index == len(parts)-1 {
							return node.Content[i].Line
						}
						return find(node.Content[i+1], index+1)
					}
				}
			}
			return 0
		}
		if line := find(root.Content[0], 0); line > 0 {
			return line
		}
	}
	return 0
}

func validationConfigError(err error, data []byte) error {
	message := err.Error()
	if message == fmt.Sprintf("健康检测间隔和超时不能超过 %d 秒", MaxHealthSeconds) {
		field := "health.interval_seconds / health.timeout_seconds"
		return safeError(ErrorValidation, field, yamlLineForField(data, field), fieldLineMessage(field, yamlLineForField(data, field), message+"。"), nil)
	}
	if strings.HasPrefix(message, "目标域名无效：") {
		field := "targets"
		line := yamlLineForField(data, field)
		return safeError(ErrorValidation, field, line, fieldLineMessage(field, line, "目标域名格式无效。请填写域名，通配子域请写成 *.域名。"), nil)
	}
	fields := map[string]string{
		"监听地址必须是环回地址":                     "server.host",
		"监听端口必须在 1 到 65535 之间":            "server.port",
		"上游类型只允许 auto、http 或 socks5":      "upstream.type",
		"上游主机名或 IP 地址无效":                  "upstream.host",
		"上游端口必须在 1 到 65535 之间":            "upstream.port",
		"上游地址不能与本地监听地址相同":                 "upstream.host",
		"日志级别只允许 debug、info、warn 或 error": "log.level",
		"日志文件路径不能为空":                      "log.file",
		"健康检测间隔和超时必须大于零":                  "health.interval_seconds / health.timeout_seconds",
		"健康状态文件路径不能为空":                    "health.state_file",
		"健康检测地址必须是有效的 HTTP 或 HTTPS 地址":    "health.url",
		"健康检测地址不能包含用户名或密码":                "health.url",
		"目标域名列表不能为空":                      "targets",
		"目标域名通配符只允许写成 *.域名":               "targets",
		"目标域名无效":                          "targets",
	}
	field := fields[message]
	if field == "" {
		field = "配置"
	}
	if _, known := fields[message]; known {
		line := yamlLineForField(data, field)
		return safeError(ErrorValidation, field, line, fieldLineMessage(field, line, message+"。"), nil)
	}
	line := yamlLineForField(data, field)
	return safeError(ErrorValidation, field, line, fieldLineMessage(field, line, "字段不符合要求。请检查该字段的格式和取值。"), nil)
}

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Upstream UpstreamConfig `yaml:"upstream"`
	Targets  []string       `yaml:"targets"`
	Log      LogConfig      `yaml:"log"`
	Health   HealthConfig   `yaml:"health"`
	Launcher LauncherConfig `yaml:"launcher"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type UpstreamConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

type HealthConfig struct {
	URL             string `yaml:"url"`
	IntervalSeconds int    `yaml:"interval_seconds"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
	StateFile       string `yaml:"state_file"`
}

// MaxHealthSeconds 限制健康检测的间隔和超时，避免转换为 time.Duration 时溢出。
const MaxHealthSeconds = 24 * 60 * 60

type LauncherConfig struct {
	Executable string   `yaml:"executable"`
	Args       []string `yaml:"args"`
}

// Default 返回适合本机使用的初始配置。
func Default() Config {
	return Config{
		Server:   ServerConfig{Host: "127.0.0.1", Port: 17890},
		Upstream: UpstreamConfig{Type: "auto", Host: "127.0.0.1", Port: 1080},
		Targets:  []string{"openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com", "oaistatic.com", "*.oaistatic.com", "oaiusercontent.com", "*.oaiusercontent.com"},
		Log:      LogConfig{Level: "info", File: "logs/codex-router.log"},
		Health:   HealthConfig{URL: "https://api.openai.com/", IntervalSeconds: 30, TimeoutSeconds: 10, StateFile: "data/health.json"},
	}
}

// Load 读取严格校验的 YAML 配置，并以配置文件目录解析相对路径。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, safeError(ErrorRead, "", 0, "读取配置文件失败，请检查文件位置和访问权限。", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, yamlConfigError(data, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, safeError(ErrorSyntax, "", 0, "配置文件只能包含一个 YAML 文档，请移除后续文档。", nil)
		}
		return nil, yamlConfigError(data, err)
	}
	if err := cfg.expandPassword(); err != nil {
		var configErr *ConfigError
		if errors.As(err, &configErr) {
			configErr.Line = yamlLineForField(data, configErr.Field)
			if configErr.Line > 0 {
				configErr.Message = fieldLineMessage(configErr.Field, configErr.Line, configErr.Message)
			}
		}
		return nil, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, safeError(ErrorRead, "", 0, "无法解析配置文件所在目录，请检查文件路径。", err)
	}
	cfg.Log.File = resolveRelative(base, cfg.Log.File)
	cfg.Health.StateFile = resolveRelative(base, cfg.Health.StateFile)
	cfg.Launcher.Executable = resolveRelative(base, cfg.Launcher.Executable)
	if err := cfg.Validate(); err != nil {
		return nil, validationConfigError(err, data)
	}
	return &cfg, nil
}

// WriteDefault 仅在目标文件不存在时写入默认配置。
func WriteDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查配置文件失败：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}
	data, err := yaml.Marshal(Default())
	if err != nil {
		return fmt.Errorf("生成默认配置失败：%w", err)
	}
	data = append([]byte("# 请将上游地址和端口替换为手机向电脑开放的代理入口，不能填写手机内部远端节点地址。\n"), data...)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("创建默认配置失败：%w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("写入默认配置失败：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭默认配置失败：%w", err)
	}
	return nil
}

// UpdateUpstream 只更新配置中的上游主机和端口，保留其余字段及注释。
// 写入采用临时文件替换，避免面板保存过程中留下半份配置。
func UpdateUpstream(path, host string, port int) error {
	host = strings.TrimSpace(host)
	if !validHost(host) {
		return validationConfigError(fmt.Errorf("上游主机名或 IP 地址无效"), nil)
	}
	if port < 1 || port > 65535 {
		return validationConfigError(fmt.Errorf("上游端口必须在 1 到 65535 之间"), nil)
	}
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if port == cfg.Server.Port && sameHost(host, cfg.Server.Host) {
		return validationConfigError(fmt.Errorf("上游地址不能与本地监听地址相同"), nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return safeError(ErrorRead, "", 0, "读取配置文件失败，请检查文件位置和访问权限。", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return yamlConfigError(data, err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("配置文件根节点必须是对象")
	}
	rootMap := root.Content[0]
	var upstream *yaml.Node
	for i := 0; i+1 < len(rootMap.Content); i += 2 {
		if rootMap.Content[i].Value == "upstream" {
			upstream = rootMap.Content[i+1]
			break
		}
	}
	if upstream == nil || upstream.Kind != yaml.MappingNode {
		return fmt.Errorf("配置缺少 upstream 节点")
	}
	set := func(key, value string) {
		for i := 0; i+1 < len(upstream.Content); i += 2 {
			if upstream.Content[i].Value == key {
				upstream.Content[i+1].Value = value
				upstream.Content[i+1].Tag = "!!str"
				return
			}
		}
		upstream.Content = append(upstream.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	set("host", host)
	set("port", fmt.Sprint(port))
	// 端口必须按数字写入，避免严格解析时被识别为字符串。
	for i := 0; i+1 < len(upstream.Content); i += 2 {
		if upstream.Content[i].Value == "port" {
			upstream.Content[i+1].Tag = "!!int"
		}
	}
	updated, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("生成配置文件失败：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-router-config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建配置临时文件失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置配置文件权限失败：%w", err)
	}
	if _, err = tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入配置文件失败：%w", err)
	}
	closeErr := tmp.Close()
	if closeErr != nil {
		return fmt.Errorf("关闭配置临时文件失败：%w", closeErr)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换配置文件失败：%w", err)
	}
	return nil
}

// Validate 检查配置字段及其取值。
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("配置不能为空")
	}
	if !isLoopback(c.Server.Host) {
		return fmt.Errorf("监听地址必须是环回地址")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("监听端口必须在 1 到 65535 之间")
	}
	switch strings.ToLower(c.Upstream.Type) {
	case "auto", "http", "socks5":
	default:
		return fmt.Errorf("上游类型只允许 auto、http 或 socks5")
	}
	if !validHost(c.Upstream.Host) {
		return fmt.Errorf("上游主机名或 IP 地址无效")
	}
	if c.Upstream.Port < 1 || c.Upstream.Port > 65535 {
		return fmt.Errorf("上游端口必须在 1 到 65535 之间")
	}
	if c.Upstream.Port == c.Server.Port && sameHost(c.Upstream.Host, c.Server.Host) {
		return fmt.Errorf("上游地址不能与本地监听地址相同")
	}
	if c.Log.Level != "debug" && c.Log.Level != "info" && c.Log.Level != "warn" && c.Log.Level != "error" {
		return fmt.Errorf("日志级别只允许 debug、info、warn 或 error")
	}
	if strings.TrimSpace(c.Log.File) == "" {
		return fmt.Errorf("日志文件路径不能为空")
	}
	if c.Health.IntervalSeconds < 1 || c.Health.TimeoutSeconds < 1 {
		return fmt.Errorf("健康检测间隔和超时必须大于零")
	}
	if c.Health.IntervalSeconds > MaxHealthSeconds || c.Health.TimeoutSeconds > MaxHealthSeconds {
		return fmt.Errorf("健康检测间隔和超时不能超过 %d 秒", MaxHealthSeconds)
	}
	if strings.TrimSpace(c.Health.StateFile) == "" {
		return fmt.Errorf("健康状态文件路径不能为空")
	}
	u, err := url.ParseRequestURI(c.Health.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("健康检测地址必须是有效的 HTTP 或 HTTPS 地址")
	}
	if u.User != nil {
		return fmt.Errorf("健康检测地址不能包含用户名或密码")
	}
	if len(c.Targets) == 0 {
		return fmt.Errorf("目标域名列表不能为空")
	}
	for _, target := range c.Targets {
		if err := validateTarget(target); err != nil {
			return err
		}
	}
	return nil
}

// ListenAddress 返回代理监听地址。
func (c *Config) ListenAddress() string {
	return net.JoinHostPort(c.Server.Host, fmt.Sprint(c.Server.Port))
}

// UpstreamAddress 返回上游代理地址。
func (c *Config) UpstreamAddress() string {
	return net.JoinHostPort(c.Upstream.Host, fmt.Sprint(c.Upstream.Port))
}

// ProxyURL 返回本地代理入口地址，供启动器设置应用代理环境变量。
func (c *Config) ProxyURL() string {
	return (&url.URL{Scheme: "http", Host: c.ListenAddress()}).String()
}

// MatchTarget 判断主机名是否属于配置的精确域名或通配子域。
func (c *Config) MatchTarget(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.Trim(h, "[]")
	}
	host = strings.TrimSuffix(host, ".")
	if !validDNSName(host) {
		return false
	}
	for _, target := range c.Targets {
		target = strings.ToLower(strings.TrimSpace(target))
		if strings.HasPrefix(target, "*.") {
			suffix := strings.TrimPrefix(target, "*")
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		} else if host == target {
			return true
		}
	}
	return false
}

func (c *Config) expandPassword() error {
	p := c.Upstream.Password
	if strings.HasPrefix(p, "${") && strings.HasSuffix(p, "}") {
		name := p[2 : len(p)-1]
		if name == "" {
			return safeError(ErrorValidation, "upstream.password", 0, "上游密码的环境变量引用不能为空。请填写变量名称，或直接留空。", nil)
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return safeError(ErrorValidation, "upstream.password", 0, "上游密码引用的环境变量未定义。请设置该环境变量，或修改密码引用。", nil)
		}
		c.Upstream.Password = value
	}
	return nil
}

func resolveRelative(base, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(base, path))
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateTarget(target string) error {
	t := strings.TrimSpace(target)
	if strings.HasPrefix(t, "*.") {
		t = strings.TrimPrefix(t, "*.")
	} else if strings.Contains(t, "*") {
		return fmt.Errorf("目标域名通配符只允许写成 *.域名")
	}
	if !validDNSName(t) {
		return fmt.Errorf("目标域名无效：%q", target)
	}
	return nil
}

func validHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/\\") || hasControl(host) {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return validDNSName(host)
}

func validDNSName(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || hasControl(host) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
	}
	return true
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func sameHost(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	if strings.EqualFold(a, "localhost") {
		return isLoopback(b)
	}
	if strings.EqualFold(b, "localhost") {
		return isLoopback(a)
	}
	ipA, ipB := net.ParseIP(a), net.ParseIP(b)
	return ipA != nil && ipB != nil && ipA.Equal(ipB)
}
