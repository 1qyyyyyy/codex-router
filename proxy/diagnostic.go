package proxy

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"codex-router/config"
)

// DiagnosticError 只提供可公开展示的阶段信息，不保留可能含凭据的底层错误。
type DiagnosticError struct {
	Stage, Protocol         string
	AuthenticationConfirmed bool
}

func (e *DiagnosticError) Error() string { return e.Protocol + "的" + e.Stage + "失败" }
func diagnosticError(stage, protocol string, _ error) error {
	return &DiagnosticError{Stage: stage, Protocol: protocol, AuthenticationConfirmed: stage == "目标连接" && protocol == "第五版套接字代理"}
}

// DiagnosticDial 复用实际拨号握手；认证拒绝或目标拒绝同样证明协议已经识别。
func DiagnosticDial(ctx context.Context, cfg *config.Config, target string) (net.Conn, string, error) {
	if cfg == nil {
		return nil, "", diagnosticError("协议", "未知代理", nil)
	}
	typ := strings.ToLower(cfg.Upstream.Type)
	types := []string{typ}
	if typ == "auto" {
		types = []string{"socks5", "http"}
	}
	d := &net.Dialer{Timeout: 3 * time.Second}
	var last error
	for index, typ := range types {
		if typ != "http" && typ != "socks5" {
			return nil, "", diagnosticError("协议", "未知代理", nil)
		}
		budget := 3 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			// 给剩余协议候选及最终目标请求各保留一份时间。
			share := time.Until(deadline) / time.Duration(len(types)-index+1)
			if share < budget {
				budget = share
			}
		}
		probeContext, cancel := context.WithTimeout(ctx, budget)
		c, err := dialUpstream(probeContext, d, cfg, typ, target)
		cancel()
		name := "网页代理"
		if typ == "socks5" {
			name = "第五版套接字代理"
		}
		if err == nil {
			return c, name, nil
		}
		var classified *DiagnosticError
		if errors.As(err, &classified) && classified.Stage != "协议" {
			return nil, name, err
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", last
}
