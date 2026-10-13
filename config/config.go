// 配置包负责读取、校验和保存路由器配置。
package config

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

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
		return nil, fmt.Errorf("读取配置文件失败：%w", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败：%w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("配置文件只能包含一个 YAML 文档")
		}
		return nil, fmt.Errorf("检查配置文件尾部失败：%w", err)
	}
	if err := cfg.expandPassword(); err != nil {
		return nil, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("解析配置目录失败：%w", err)
	}
	cfg.Log.File = resolveRelative(base, cfg.Log.File)
	cfg.Health.StateFile = resolveRelative(base, cfg.Health.StateFile)
	cfg.Launcher.Executable = resolveRelative(base, cfg.Launcher.Executable)
	if err := cfg.Validate(); err != nil {
		return nil, err
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
			return fmt.Errorf("上游密码环境变量名称不能为空")
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return fmt.Errorf("上游密码引用的环境变量 %q 未定义", name)
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
