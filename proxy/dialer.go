package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"codex-router/config"
)

// NewUpstreamDialer 返回依据配置建立直连或上游隧道的拨号函数。
func NewUpstreamDialer(cfg *config.Config) (func(context.Context, string, string) (net.Conn, error), error) {
	dial, _, err := NewUpstreamDialerWithProtocol(cfg)
	return dial, err
}

// NewUpstreamDialerWithProtocol 还返回自动识别到的上游协议名称。
func NewUpstreamDialerWithProtocol(cfg *config.Config) (func(context.Context, string, string) (net.Conn, error), func() string, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("代理配置不能为空")
	}
	typ := strings.ToLower(cfg.Upstream.Type)
	if typ != "auto" && typ != "http" && typ != "socks5" {
		return nil, nil, fmt.Errorf("不支持的上游类型")
	}
	var mu sync.Mutex
	selected := typ
	if typ == "auto" {
		selected = ""
	}
	protocol := func() string { mu.Lock(); defer mu.Unlock(); return selected }
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if typ == "http" {
			c, err := dialHTTPConnect(ctx, dialer, cfg, address)
			if err != nil {
				return nil, fmt.Errorf("上游 HTTP CONNECT 连接或握手失败")
			}
			return c, nil
		}
		if typ == "socks5" {
			c, err := dialSOCKS5(ctx, dialer, cfg, address)
			if err != nil {
				return nil, fmt.Errorf("上游 SOCKS5 连接或握手失败")
			}
			return c, nil
		}
		mu.Lock()
		choice := selected
		mu.Unlock()
		if choice != "" {
			c, err := dialUpstream(ctx, dialer, cfg, choice, address)
			if err == nil {
				return c, nil
			}
			if ctx.Err() != nil {
				return nil, fmt.Errorf("上游连接已取消")
			}
			other := "socks5"
			if choice == "socks5" {
				other = "http"
			}
			c, second := dialWithProbeTimeout(ctx, dialer, cfg, other, address)
			if second == nil {
				mu.Lock()
				selected = other
				mu.Unlock()
				return c, nil
			}
			return nil, fmt.Errorf("已识别的上游协议和备用协议均连接失败：%s；%s", safeError(err), safeError(second))
		}
		var errs []string
		for _, candidate := range []string{"socks5", "http"} {
			c, err := dialWithProbeTimeout(ctx, dialer, cfg, candidate, address)
			if err == nil {
				mu.Lock()
				selected = candidate
				mu.Unlock()
				return c, nil
			}
			if ctx.Err() != nil {
				return nil, fmt.Errorf("上游协议探测已取消")
			}
			errs = append(errs, candidate+"："+safeError(err))
		}
		return nil, fmt.Errorf("自动识别上游协议失败：%s", strings.Join(errs, "；"))
	}
	return dial, protocol, nil
}

func safeError(err error) string {
	if err == nil {
		return "未知错误"
	}
	return "上游连接或协议握手失败"
}

func dialWithProbeTimeout(ctx context.Context, d *net.Dialer, cfg *config.Config, typ, address string) (net.Conn, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return dialUpstream(cctx, d, cfg, typ, address)
}

func dialUpstream(ctx context.Context, d *net.Dialer, cfg *config.Config, typ, address string) (net.Conn, error) {
	if typ == "http" {
		return dialHTTPConnect(ctx, d, cfg, address)
	}
	return dialSOCKS5(ctx, d, cfg, address)
}

func dialHTTPConnect(ctx context.Context, d *net.Dialer, cfg *config.Config, target string) (connection net.Conn, resultErr error) {
	stage := "协议"
	defer func() {
		if resultErr != nil {
			resultErr = diagnosticError(stage, "网页代理", resultErr)
		}
	}()
	if err := validateProxyTarget(target); err != nil {
		return nil, err
	}
	ctx, cancel := boundedHandshakeContext(ctx)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", cfg.UpstreamAddress())
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	failed := true
	defer func() {
		if failed {
			_ = c.Close()
		}
	}()
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if cfg.Upstream.Username != "" || cfg.Upstream.Password != "" {
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", basicAuth(cfg.Upstream.Username, cfg.Upstream.Password))
	}
	b.WriteString("\r\n")
	if _, err = io.WriteString(c, b.String()); err != nil {
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := readHTTPStatus(br)
	if err != nil {
		return nil, fmt.Errorf("读取上游 HTTP CONNECT 响应失败")
	}
	if resp != 200 {
		stage = "目标连接"
		if resp == 407 {
			stage = "认证"
		}
		return nil, fmt.Errorf("上游 HTTP CONNECT 返回状态 %d", resp)
	}
	_ = c.SetDeadline(time.Time{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !stop() {
		return nil, context.Canceled
	}
	failed = false
	return &bufferedConn{Conn: c, reader: br}, nil
}

func readHTTPStatus(br *bufio.Reader) (int, error) {
	total := 0
	line, err := readBoundedLine(br, &total)
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(line)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0, fmt.Errorf("上游返回了无效 HTTP 状态行")
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("上游返回了无效 HTTP 状态码")
	}
	for {
		line, err = readBoundedLine(br, &total)
		if err != nil {
			return 0, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return status, nil
}

func readBoundedLine(br *bufio.Reader, total *int) (string, error) {
	var out strings.Builder
	for {
		part, err := br.ReadSlice('\n')
		*total += len(part)
		if *total > 64*1024 {
			return "", fmt.Errorf("上游响应头过大")
		}
		out.Write(part)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return "", err
		}
		return out.String(), nil
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return nil
}
func (c *bufferedConn) CloseRead() error {
	if v, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return v.CloseRead()
	}
	return nil
}

func dialSOCKS5(ctx context.Context, d *net.Dialer, cfg *config.Config, target string) (connection net.Conn, resultErr error) {
	stage := "协议"
	defer func() {
		if resultErr != nil {
			resultErr = diagnosticError(stage, "第五版套接字代理", resultErr)
		}
	}()
	if err := validateProxyTarget(target); err != nil {
		return nil, err
	}
	ctx, cancel := boundedHandshakeContext(ctx)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", cfg.UpstreamAddress())
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	failed := true
	defer func() {
		if failed {
			_ = c.Close()
		}
	}()
	methods := []byte{0}
	if cfg.Upstream.Username != "" || cfg.Upstream.Password != "" {
		methods = append(methods, 2)
	}
	if _, err = c.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err = io.ReadFull(c, greeting[:]); err != nil {
		return nil, err
	}
	if greeting[0] != 5 {
		return nil, fmt.Errorf("上游返回了错误的协议版本")
	}
	stage = "认证"
	if greeting[1] == 0xff {
		return nil, fmt.Errorf("SOCKS5 上游未接受认证方式")
	}
	if greeting[1] == 2 {
		if len(methods) < 2 || methods[1] != 2 || (cfg.Upstream.Username == "" && cfg.Upstream.Password == "") {
			return nil, fmt.Errorf("SOCKS5 上游选择了未允许的认证方式")
		}
		if len(cfg.Upstream.Username) > 255 || len(cfg.Upstream.Password) > 255 {
			return nil, fmt.Errorf("SOCKS5 用户名或密码过长")
		}
		msg := []byte{1, byte(len(cfg.Upstream.Username))}
		msg = append(msg, cfg.Upstream.Username...)
		msg = append(msg, byte(len(cfg.Upstream.Password)))
		msg = append(msg, cfg.Upstream.Password...)
		if _, err = c.Write(msg); err != nil {
			return nil, err
		}
		var auth [2]byte
		if _, err = io.ReadFull(c, auth[:]); err != nil {
			return nil, err
		}
		if auth[0] != 1 || auth[1] != 0 {
			return nil, fmt.Errorf("SOCKS5 用户名密码认证失败")
		}
	} else if greeting[1] != 0 {
		return nil, fmt.Errorf("SOCKS5 上游选择了不支持的认证方式")
	}
	stage = "目标连接"
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("目标地址无效")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("目标端口无效")
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 1)
			req = append(req, v4...)
		} else {
			req = append(req, 4)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("目标域名过长")
		}
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	}
	var pbuf [2]byte
	binary.BigEndian.PutUint16(pbuf[:], uint16(port))
	req = append(req, pbuf[:]...)
	if _, err = c.Write(req); err != nil {
		return nil, err
	}
	var head [4]byte
	if _, err = io.ReadFull(c, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 5 || head[1] != 0 || head[2] != 0 {
		return nil, fmt.Errorf("SOCKS5 上游连接目标失败，状态 %d", head[1])
	}
	var n int
	switch head[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var l [1]byte
		if _, err = io.ReadFull(c, l[:]); err != nil {
			return nil, err
		}
		n = int(l[0])
	default:
		return nil, fmt.Errorf("SOCKS5 上游返回了无效地址")
	}
	if _, err = io.CopyN(io.Discard, c, int64(n+2)); err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !stop() {
		return nil, context.Canceled
	}
	failed = false
	return c, nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func boundedHandshakeContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 10*time.Second)
}
func validateProxyTarget(target string) error {
	if strings.ContainsAny(target, "\r\n") {
		return fmt.Errorf("目标地址包含无效字符")
	}
	host, text, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return fmt.Errorf("目标地址无效")
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("目标端口无效")
	}
	return nil
}
