package diagnosis

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"codex-router/config"
)

// 模拟入口会接受端口检查的空连接，并对后续连接进行真实握手。
func simulated(t *testing.T, handle func(net.Conn)) *config.Config {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); c.SetDeadline(time.Now().Add(2 * time.Second)); handle(c) }()
		}
	}()
	h, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	cfg := config.Default()
	cfg.Upstream.Host = h
	cfg.Upstream.Port = port
	cfg.Upstream.Type = "http"
	cfg.Health.URL = "http://target.invalid/private?秘密=不可泄漏"
	cfg.Health.TimeoutSeconds = 3
	cfg.Upstream.Password = "不可泄漏密码"
	return &cfg
}
func webReply(code, target int) func(net.Conn) {
	return func(c net.Conn) {
		r := bufio.NewReader(c)
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "CONNECT ") {
			return
		}
		for {
			s, e := r.ReadString('\n')
			if e != nil {
				return
			}
			if s == "\r\n" {
				break
			}
		}
		fmt.Fprintf(c, "HTTP/1.1 %d 说明\r\n\r\n", code)
		if code != 200 {
			return
		}
		for {
			s, e := r.ReadString('\n')
			if e != nil {
				return
			}
			if s == "\r\n" {
				break
			}
		}
		fmt.Fprintf(c, "HTTP/1.1 %d 说明\r\nContent-Length: 0\r\n\r\n", target)
	}
}
func TestWebDiagnosis(t *testing.T) {
	for _, tc := range []struct {
		name          string
		proxy, target int
		failed        int
		healthy       bool
	}{{"代理认证失败", 407, 200, 2, false}, {"代理目标拒绝", 403, 200, 3, false}, {"目标权限拒绝", 200, 403, -1, true}, {"目标服务故障", 200, 503, 3, false}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := simulated(t, webReply(tc.proxy, tc.target))
			r := Check(context.Background(), cfg)
			if r.Healthy != tc.healthy {
				t.Fatalf("健康判断错误：%s", r.String())
			}
			if tc.failed >= 0 && r.Steps[tc.failed].Status != "失败" {
				t.Fatalf("分层错误：%s", r.String())
			}
			if tc.proxy == 407 && r.Steps[3].Status != "跳过" {
				t.Fatal("认证失败后未跳过目标")
			}
			if tc.proxy == 403 && r.Steps[2].Status == "通过" {
				t.Fatal("未确认的认证被误报通过")
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "不可泄漏") || strings.Contains(r.String(), "private") {
				t.Fatal("诊断泄漏敏感信息")
			}
		})
	}
}
func TestClosedPort(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	h, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	port, _ := strconv.Atoi(p)
	cfg := config.Default()
	cfg.Upstream.Host = h
	cfg.Upstream.Port = port
	r := Check(context.Background(), &cfg)
	if r.Steps[0].Status != "失败" || r.Steps[1].Status != "跳过" {
		t.Fatal(r.String())
	}
}
func TestWrongProtocol(t *testing.T) {
	cfg := simulated(t, func(c net.Conn) {
		var b [1]byte
		if _, e := c.Read(b[:]); e == nil {
			io.WriteString(c, "这不是代理协议\r\n\r\n")
		}
	})
	r := Check(context.Background(), cfg)
	if r.Steps[1].Status != "失败" || r.Steps[2].Status != "跳过" {
		t.Fatal(r.String())
	}
}
func TestAutomaticProtocol(t *testing.T) {
	cfg := simulated(t, webReply(200, 401))
	cfg.Upstream.Type = "auto"
	r := Check(context.Background(), cfg)
	if !r.Healthy || !strings.Contains(r.Steps[1].Detail, "网页代理") {
		t.Fatal(r.String())
	}
}
func TestSocketAuthentication(t *testing.T) {
	cfg := simulated(t, func(c net.Conn) {
		var head [2]byte
		if _, e := io.ReadFull(c, head[:]); e != nil {
			return
		}
		io.CopyN(io.Discard, c, int64(head[1]))
		c.Write([]byte{5, 2})
		var auth [2]byte
		if _, e := io.ReadFull(c, auth[:]); e != nil {
			return
		}
		io.CopyN(io.Discard, c, int64(auth[1]))
		var n [1]byte
		if _, e := io.ReadFull(c, n[:]); e != nil {
			return
		}
		io.CopyN(io.Discard, c, int64(n[0]))
		c.Write([]byte{1, 1})
	})
	cfg.Upstream.Type = "auto"
	cfg.Upstream.Username = "用户名"
	r := Check(context.Background(), cfg)
	if r.Steps[1].Status != "通过" || r.Steps[2].Status != "失败" || r.Steps[3].Status != "跳过" {
		t.Fatal(r.String())
	}
}
func TestCanceled(t *testing.T) {
	cfg := simulated(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	r := Check(ctx, cfg)
	if time.Since(start) > time.Second || r.Healthy {
		t.Fatal("取消未及时生效")
	}
}

func TestSocketTargetFailure(t *testing.T) {
	cfg := simulated(t, func(c net.Conn) {
		var greeting [2]byte
		if _, err := io.ReadFull(c, greeting[:]); err != nil {
			return
		}
		io.CopyN(io.Discard, c, int64(greeting[1]))
		c.Write([]byte{5, 0})
		var request [4]byte
		if _, err := io.ReadFull(c, request[:]); err != nil {
			return
		}
		var length [1]byte
		if _, err := io.ReadFull(c, length[:]); err != nil {
			return
		}
		io.CopyN(io.Discard, c, int64(length[0])+2)
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
	})
	cfg.Upstream.Type = "socks5"
	r := Check(context.Background(), cfg)
	if r.Steps[2].Status != "通过" || r.Steps[3].Status != "失败" {
		t.Fatal(r.String())
	}
}

func TestAutomaticShortTimeout(t *testing.T) {
	cfg := simulated(t, func(c net.Conn) {
		reader := bufio.NewReader(c)
		first, err := reader.Peek(1)
		if err != nil {
			return
		}
		if first[0] == 5 {
			io.Copy(io.Discard, reader)
			return
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		io.WriteString(c, "HTTP/1.1 200 说明\r\n\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		io.WriteString(c, "HTTP/1.1 200 说明\r\nContent-Length: 0\r\n\r\n")
	})
	cfg.Upstream.Type = "AUTO"
	cfg.Health.TimeoutSeconds = 1
	r := Check(context.Background(), cfg)
	if !r.Healthy {
		t.Fatal(r.String())
	}
}
