// 健康检测包负责通过上游代理检测节点并原子保存状态。
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codex-router/config"
)

// Result 是一次健康检测的可持久化结果。
type Result struct {
	Time       time.Time `json:"时间"`
	Node       string    `json:"节点名"`
	LatencyMS  int64     `json:"延迟毫秒"`
	Status     string    `json:"状态"`
	Error      string    `json:"错误,omitempty"`
	StatusCode int       `json:"状态码,omitempty"`
}

// Checker 持有检测配置和强制通过上游的拨号函数。
type Checker struct {
	cfg    *config.Config
	dial   func(context.Context, string, string) (net.Conn, error)
	logger *slog.Logger
}

// New 创建健康检测器。
func New(cfg *config.Config, dial func(context.Context, string, string) (net.Conn, error), logger *slog.Logger) *Checker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Checker{cfg: cfg, dial: dial, logger: logger}
}

// Check 通过上游发起一次 HTTP 检测，且不会跟随重定向。
func (c *Checker) Check(ctx context.Context) (result Result) {
	started := time.Now()
	result = Result{Time: started, Status: "异常"}
	if c.cfg != nil {
		result.Node = c.cfg.Upstream.Name
	}
	defer func() {
		result.LatencyMS = time.Since(started).Milliseconds()
		if err := c.save(result); err != nil {
			c.logger.Error("保存健康状态失败", "错误", err)
		}
	}()
	if c.cfg == nil || c.dial == nil {
		result.Error = "健康检测配置或上游拨号器为空"
		return result
	}
	if c.cfg.Health.TimeoutSeconds < 1 || c.cfg.Health.TimeoutSeconds > config.MaxHealthSeconds {
		result.Error = "健康检测超时配置无效"
		return result
	}
	if result.Node == "" {
		result.Node = c.cfg.Upstream.Host
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.Health.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return c.dial(ctx, network, address)
		},
		TLSHandshakeTimeout:   time.Duration(c.cfg.Health.TimeoutSeconds) * time.Second,
		ResponseHeaderTimeout: time.Duration(c.cfg.Health.TimeoutSeconds) * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: time.Duration(c.cfg.Health.TimeoutSeconds) * time.Second,
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, c.cfg.Health.URL, nil)
	if err != nil {
		result.Error = "创建健康检测请求失败，请检查检测地址"
		return result
	}
	resp, err := client.Do(req)
	if err != nil {
		// 网络库错误可能包含完整 URL，其中可能带有查询参数，因此不输出原始错误。
		result.Error = "通过上游进行健康检测失败：连接、超时或 TLS 握手异常"
		return result
	}
	result.StatusCode = resp.StatusCode
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); err != nil {
		_ = resp.Body.Close()
		result.Error = "读取健康检测响应失败"
		return result
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		result.Error = fmt.Sprintf("检测服务返回服务器错误：HTTP %d", resp.StatusCode)
		return result
	}
	result.Status = "健康"
	return result
}

// Run 立即检测一次，之后按配置间隔重复检测，直到上下文取消。
func (c *Checker) Run(ctx context.Context) {
	if c.cfg == nil || c.cfg.Health.IntervalSeconds < 1 || c.cfg.Health.IntervalSeconds > config.MaxHealthSeconds {
		c.logger.Error("健康检测间隔配置无效")
		return
	}
	ticker := time.NewTicker(time.Duration(c.cfg.Health.IntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		c.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Checker) save(result Result) error {
	if c.cfg == nil || c.cfg.Health.StateFile == "" {
		return fmt.Errorf("健康状态文件路径为空")
	}
	path := c.cfg.Health.StateFile
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建健康状态目录失败：%w", err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("编码健康状态失败：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".health-*.tmp")
	if err != nil {
		return fmt.Errorf("创建健康状态临时文件失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置健康状态文件权限失败：%w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入健康状态失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步健康状态失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭健康状态临时文件失败：%w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换健康状态文件失败：%w", err)
	}
	return nil
}
