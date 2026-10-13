package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"codex-router/config"
)

func testConfig(typ string, upstream net.Listener) *config.Config {
	h, p, _ := net.SplitHostPort(upstream.Addr().String())
	var port int
	fmt.Sscan(p, &port)
	c := config.Default()
	c.Upstream = config.UpstreamConfig{Type: typ, Host: h, Port: port}
	c.Targets = []string{"target.test"}
	return &c
}

func TestHTTPUpstreamAndCredentialsStayAtProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("代理认证泄漏至源站")
		}
		if r.Header.Get("Authorization") != "Bearer source-secret" {
			t.Error("源站认证未被保留")
		}
		_, _ = io.WriteString(w, "流式响应")
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	originHost := originURL.Host
	proxy := newHTTPConnectMock(t, originHost)
	cfg := testConfig("http", proxy)
	cfg.Upstream.Username = "上游用户"
	cfg.Upstream.Password = "上游密码"
	var logs bytes.Buffer
	s, err := New(cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(ln)
	defer s.Shutdown(context.Background())
	purl, _ := url.Parse("http://" + ln.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(purl)}}
	req, _ := http.NewRequest(http.MethodGet, "http://target.test:80/resource?secret=query", nil)
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	req.Header.Set("Authorization", "Bearer source-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "流式响应" {
		t.Fatalf("响应异常：%q", body)
	}
	if got := <-proxy.auth; got != "Basic "+basicAuth(cfg.Upstream.Username, cfg.Upstream.Password) {
		t.Fatalf("上游认证不正确：%q", got)
	}
	for _, secret := range []string{"query", "secret=value", "上游密码", "source-secret", "流式响应"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("日志泄漏了敏感内容 %q", secret)
		}
	}
}

func TestHandlerRejectsRequestWithoutURL(t *testing.T) {
	request := &http.Request{Method: http.MethodGet, Host: "example.com"}
	recorder := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少 URL 的请求应返回 400，实际为 %d", recorder.Code)
	}
}

func TestNonTargetUsesDirectConnection(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "直连") }))
	defer origin.Close()
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	h, p, _ := net.SplitHostPort(deadAddr)
	var port int
	_, _ = fmt.Sscan(p, &port)
	cfg := config.Default()
	cfg.Upstream = config.UpstreamConfig{Type: "socks5", Host: h, Port: port}
	cfg.Targets = []string{"target.test"}
	s, err := New(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(ln)
	defer s.Shutdown(context.Background())
	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "直连" {
		t.Fatalf("直连结果异常：%q", body)
	}
}

func TestHTTPResponseStreamsWithoutBuffering(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "甲")
		w.(http.Flusher).Flush()
		time.Sleep(600 * time.Millisecond)
		_, _ = io.WriteString(w, "乙")
	}))
	defer origin.Close()
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()
	_, p, _ := net.SplitHostPort(deadAddr)
	var port int
	_, _ = fmt.Sscan(p, &port)
	cfg := config.Default()
	cfg.Upstream = config.UpstreamConfig{Type: "socks5", Host: "127.0.0.1", Port: port}
	cfg.Targets = []string{"target.test"}
	s, err := New(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(ln)
	defer s.Shutdown(context.Background())
	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	started := time.Now()
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len("甲"))
	if _, err = io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 400*time.Millisecond {
		t.Fatal("小块响应未及时传给客户端")
	}
}

func TestSOCKS5AndAutoProtocol(t *testing.T) {
	echo := startEcho(t)
	defer echo.Close()
	socks := newSOCKS5Mock(t, echo.Addr().String(), true)
	for _, typ := range []string{"socks5", "auto"} {
		cfg := testConfig(typ, socks)
		cfg.Upstream.Username = "u"
		cfg.Upstream.Password = "p"
		dial, detected, err := NewUpstreamDialerWithProtocol(cfg)
		if err != nil {
			t.Fatal(err)
		}
		c, err := dial(context.Background(), "tcp", "target.test:443")
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if _, err = c.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 7)
		if _, err = io.ReadFull(c, b); err != nil {
			t.Fatal(err)
		}
		c.Close()
		if string(b) != "payload" {
			t.Fatalf("回显异常：%q", b)
		}
		if typ == "auto" && detected() != "socks5" {
			t.Fatalf("自动识别结果错误：%q", detected())
		}
	}
}

func TestAutoFallsBackBetweenProtocolsAndNeverDirect(t *testing.T) {
	echo := startEcho(t)
	defer echo.Close()
	// 此端口不是 SOCKS5，但提供有效的 HTTP CONNECT 服务，因此自动探测应切换协议。
	httpProxy := newHTTPConnectMock(t, echo.Addr().String())
	cfg := testConfig("auto", httpProxy)
	dial, detected, err := NewUpstreamDialerWithProtocol(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(context.Background(), "tcp", "target.test:443")
	if err != nil {
		t.Fatal(err)
	}
	if detected() != "http" {
		t.Fatalf("自动识别结果错误：%q", detected())
	}
	c.Close()
	// 上游不可达时必须失败，不可尝试连接目标地址。
	bad, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := bad.Addr().String()
	bad.Close()
	cfg.Upstream.Host = "127.0.0.1"
	_, portText, _ := net.SplitHostPort(addr)
	fmt.Sscan(portText, &cfg.Upstream.Port)
	failed, err := NewUpstreamDialer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = failed(ctx, "tcp", echo.Addr().String()); err == nil {
		t.Fatal("上游失败时意外直连成功")
	}
}

func TestSOCKS5RejectsNonzeroReservedByte(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var greeting [2]byte
		if _, e = io.ReadFull(c, greeting[:]); e != nil {
			return
		}
		methods := make([]byte, greeting[1])
		if _, e = io.ReadFull(c, methods); e != nil {
			return
		}
		_, _ = c.Write([]byte{5, 0})
		var req [4]byte
		if _, e = io.ReadFull(c, req[:]); e != nil {
			return
		}
		addr := make([]byte, 0)
		switch req[3] {
		case 1:
			addr = make([]byte, 4)
		case 3:
			var n [1]byte
			if _, e = io.ReadFull(c, n[:]); e != nil {
				return
			}
			addr = make([]byte, n[0])
		case 4:
			addr = make([]byte, 16)
		}
		if _, e = io.ReadFull(c, addr); e != nil {
			return
		}
		var port [2]byte
		if _, e = io.ReadFull(c, port[:]); e != nil {
			return
		}
		_, _ = c.Write([]byte{5, 0, 1, 1, 127, 0, 0, 1, 0, 0})
	}()
	cfg := testConfig("socks5", ln)
	dial, err := NewUpstreamDialer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dial(context.Background(), "tcp", "target.test:443"); err == nil {
		t.Fatal("SOCKS5 非零保留字节未被拒绝")
	}
}

func TestHTTPConnectBufferedBytesAndHalfClose(t *testing.T) {
	echo := startHalfClose(t)
	defer echo.Close()
	upstream := newSOCKS5Mock(t, echo.Addr().String(), false)
	cfg := testConfig("socks5", upstream)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(ln)
	defer s.Shutdown(context.Background())
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT target.test:443 HTTP/1.1\r\nHost: target.test:443\r\n\r\nDATA")
	br := bufio.NewReader(c)
	if _, err = br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	for {
		line, e := br.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		if line == "\r\n" {
			break
		}
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		if err := tcp.CloseWrite(); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("客户端连接不支持半关闭")
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	tail, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "TAIL" {
		t.Fatalf("半关闭后源站尾部数据丢失：%q", tail)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("服务关闭后隧道仍保持打开")
	}
	_ = c.Close()
}

func TestCONNECTCarriesTLSWithoutInterception(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/encrypted" {
			t.Errorf("源站收到错误路径：%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "TLS 通道有效")
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	upstream := newHTTPConnectMock(t, originURL.Host)
	cfg := testConfig("http", upstream)
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(ln)
	defer s.Shutdown(context.Background())
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(conn, "CONNECT target.test:443 HTTP/1.1\r\nHost: target.test:443\r\n\r\n")
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT 未成功：%s", status)
	}
	for {
		line, e := br.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		if line == "\r\n" {
			break
		}
	}
	tlsConn := tls.Client(&bufferedConn{Conn: conn, reader: br}, &tls.Config{InsecureSkipVerify: true, ServerName: "target.test"})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tlsConn, "GET /encrypted?secret=value HTTP/1.1\r\nHost: target.test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "TLS 通道有效" {
		t.Fatalf("TLS 隧道数据异常：%q", body)
	}
}

func TestCancellationStopsAutoProbe(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	cfg := testConfig("auto", ln)
	dial, _ := NewUpstreamDialer(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := dial(ctx, "tcp", "target.test:80")
	if err == nil {
		t.Fatal("取消后仍然成功")
	}
	if time.Since(start) > time.Second {
		t.Fatal("取消未能及时结束握手")
	}
}

func TestHTTPUpstreamPreReadBytesArePreserved(t *testing.T) {
	ln := newHTTPPreReadMock(t)
	defer ln.Close()
	cfg := testConfig("http", ln)
	dial, err := NewUpstreamDialer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(context.Background(), "tcp", "target.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 5)
	if _, err = io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "EARLY" {
		t.Fatalf("HTTP CONNECT 上游预读字节丢失：%q", buf)
	}
}

func TestConnectFailureFlushesBadGateway(t *testing.T) {
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := dead.Addr().String()
	dead.Close()
	_, p, _ := net.SplitHostPort(addr)
	var port int
	_, _ = fmt.Sscan(p, &port)
	cfg := config.Default()
	cfg.Upstream = config.UpstreamConfig{Type: "socks5", Host: "127.0.0.1", Port: port}
	cfg.Targets = []string{"target.test"}
	s, err := New(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go s.Serve(ln)
	defer s.Shutdown(context.Background())
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(c, "CONNECT target.test:443 HTTP/1.1\r\nHost: target.test:443\r\n\r\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "502") {
		t.Fatalf("期望立即收到 502，得到 %q", line)
	}
}

func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln
}

func startHalfClose(t *testing.T) net.Listener {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(io.Discard, c); _, _ = io.WriteString(c, "TAIL") }()
		}
	}()
	return ln
}

func newHTTPPreReadMock(t *testing.T) net.Listener {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, e := br.ReadString('\n')
					if e != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\nEARLY")
				_, _ = io.Copy(io.Discard, br)
			}(c)
		}
	}()
	return ln
}

type httpMock struct {
	net.Listener
	auth chan string
}

func newHTTPConnectMock(t *testing.T, target string) *httpMock {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	m := &httpMock{Listener: ln, auth: make(chan string, 8)}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				line, e := br.ReadString('\n')
				if e != nil {
					return
				}
				_ = line
				auth := ""
				for {
					line, e = br.ReadString('\n')
					if e != nil {
						return
					}
					if strings.HasPrefix(strings.ToLower(line), "proxy-authorization:") {
						auth = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
					}
					if line == "\r\n" {
						break
					}
				}
				select {
				case m.auth <- auth:
				default:
				}
				up, e := net.Dial("tcp", target)
				if e != nil {
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go func() { _, _ = io.Copy(up, br); closeWrite(up) }()
				_, _ = io.Copy(c, up)
			}(c)
		}
	}()
	return m
}

func newSOCKS5Mock(t *testing.T, target string, auth bool) net.Listener {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var h [2]byte
				if _, e := io.ReadFull(c, h[:]); e != nil {
					return
				}
				methods := make([]byte, int(h[1]))
				if _, e = io.ReadFull(c, methods); e != nil {
					return
				}
				method := byte(0)
				if auth {
					method = 2
				}
				_, _ = c.Write([]byte{5, method})
				if method == 2 {
					var a [2]byte
					if _, e = io.ReadFull(c, a[:]); e != nil {
						return
					}
					u := make([]byte, int(a[1]))
					if _, e = io.ReadFull(c, u); e != nil {
						return
					}
					var n [1]byte
					if _, e = io.ReadFull(c, n[:]); e != nil {
						return
					}
					p := make([]byte, int(n[0]))
					if _, e = io.ReadFull(c, p); e != nil {
						return
					}
					if string(u) != "u" || string(p) != "p" {
						_, _ = c.Write([]byte{1, 1})
						return
					}
					_, _ = c.Write([]byte{1, 0})
				}
				var req [4]byte
				if _, e = io.ReadFull(c, req[:]); e != nil {
					return
				}
				switch req[3] {
				case 1:
					_, _ = io.CopyN(io.Discard, c, 4)
				case 4:
					_, _ = io.CopyN(io.Discard, c, 16)
				case 3:
					var n [1]byte
					if _, e = io.ReadFull(c, n[:]); e != nil {
						return
					}
					name := make([]byte, int(n[0]))
					if _, e = io.ReadFull(c, name); e != nil {
						return
					}
					if string(name) != "target.test" {
						return
					}
				}
				var port [2]byte
				if _, e = io.ReadFull(c, port[:]); e != nil {
					return
				}
				_ = binary.BigEndian.Uint16(port[:])
				up, e := net.Dial("tcp", target)
				if e != nil {
					return
				}
				defer up.Close()
				_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go func() { _, _ = io.Copy(up, c); closeWrite(up) }()
				_, _ = io.Copy(c, up)
			}(c)
		}
	}()
	return ln
}
