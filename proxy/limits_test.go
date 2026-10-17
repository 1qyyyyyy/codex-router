package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-router/config"
)

func limitedProxy(t *testing.T, limit int) (*Server, *http.Client, string) {
	t.Helper()
	cfg := config.Default()
	s, err := New(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.requestSlots = make(chan struct{}, limit)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(listener)
	proxyURL, _ := url.Parse("http://" + listener.Addr().String())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("停止测试代理失败：%v", err)
		}
	})
	return s, client, listener.Addr().String()
}

func waitRequestSlots(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.requestSlots) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("活动请求额度未归还：当前 %d，预期 %d", len(s.requestSlots), want)
}

func TestResponseHeaderWaitIsBounded(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer origin.Close()
	s, client, _ := limitedProxy(t, 1)
	s.transport.ResponseHeaderTimeout = 100 * time.Millisecond
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("响应头超时应返回连接失败，得到 %d", response.StatusCode)
	}
	waitRequestSlots(t, s, 0)
}

func TestResponseHeaderDeadlineDoesNotCutOffStreamingBody(t *testing.T) {
	continueBody := make(chan struct{})
	resume := sync.OnceFunc(func() { close(continueBody) })
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "甲")
		w.(http.Flusher).Flush()
		select {
		case <-continueBody:
			_, _ = io.WriteString(w, "乙")
		case <-r.Context().Done():
		}
	}))
	defer origin.Close()
	defer resume()
	s, client, _ := limitedProxy(t, 1)
	s.transport.ResponseHeaderTimeout = 100 * time.Millisecond
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("甲"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "甲" {
		t.Fatalf("首块响应异常：%q，%v", first, err)
	}
	excess, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	excess.Body.Close()
	if excess.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("传输中的正文应继续占用额度，得到 %d", excess.StatusCode)
	}
	// 正文停顿超过响应头等待时间后，仍应能够继续传输。
	time.Sleep(300 * time.Millisecond)
	resume()
	rest, err := io.ReadAll(response.Body)
	if err != nil || string(rest) != "乙" {
		t.Fatalf("流式正文被截断：%q，%v", rest, err)
	}
	waitRequestSlots(t, s, 0)
}

func TestActiveRequestLimitRejectsExcessAndRecoversAfterCancellation(t *testing.T) {
	entered := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "恢复成功")
	}))
	defer origin.Close()
	s, client, _ := limitedProxy(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/hold", nil)
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		if response, err := client.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("首个请求没有到达目标")
	}
	response, err := client.Get(origin.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") == "" {
		t.Fatalf("超限请求应立即返回可重试状态，得到 %d", response.StatusCode)
	}
	cancel()
	<-completed
	waitRequestSlots(t, s, 0)
	response, err = client.Get(origin.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("取消后额度未恢复，得到 %d", response.StatusCode)
	}
}

func TestTunnelHoldsRequestSlotUntilConnectionCloses(t *testing.T) {
	echo := startEcho(t)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + dead.Addr().String() + "/"
	dead.Close()
	s, client, proxyAddress := limitedProxy(t, 1)
	tunnel, err := net.DialTimeout("tcp", proxyAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	_ = tunnel.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(tunnel, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr())
	reader := bufio.NewReader(tunnel)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		t.Fatalf("隧道建立失败：%q，%v", line, err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	response, err := client.Get(deadURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("活动隧道未占用额度，得到 %d", response.StatusCode)
	}
	tunnel.Close()
	waitRequestSlots(t, s, 0)
	// 拨号失败也应归还额度，避免失败请求逐步耗尽连接容量。
	response, err = client.Get(deadURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("隧道关闭后未恢复处理请求，得到 %d", response.StatusCode)
	}
	waitRequestSlots(t, s, 0)
}
