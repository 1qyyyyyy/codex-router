package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-router/config"
)

func TestCheckTreatsUnauthorizedAsReachableAndPersistsAtomically(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 延迟响应，确认返回值包含本次检测耗时。
		select {
		case <-time.After(15 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		if r.URL.Path != "/probe" {
			t.Errorf("检测路径错误：%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "nested", "health.json")
	cfg := config.Default()
	cfg.Health.URL = server.URL + "/probe"
	cfg.Health.TimeoutSeconds = 2
	cfg.Health.StateFile = statePath
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	result := New(&cfg, dial, nil).Check(context.Background())
	if result.Status != "健康" || result.StatusCode != http.StatusUnauthorized || result.Node == "" || result.LatencyMS < 1 {
		t.Fatalf("401 应表示上游连通：%+v", result)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved Result
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("状态文件不是有效 JSON：%v", err)
	}
	if saved.Status != "健康" || saved.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态文件未保存检测结果：%+v", saved)
	}
	if !strings.Contains(string(data), `"状态"`) {
		t.Fatalf("状态文件字段应使用中文：%s", data)
	}
	entries, err := os.ReadDir(filepath.Dir(statePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(statePath) {
		t.Fatalf("原子保存后应只留下状态文件：%v", entries)
	}
}

func TestCheckDoesNotFollowRedirectOrAcceptServerError(t *testing.T) {
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/destination", http.StatusFound)
		case "/destination":
			redirected = true
			w.WriteHeader(http.StatusOK)
		case "/failure":
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 2
	cfg.Health.StateFile = filepath.Join(t.TempDir(), "health.json")
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	checker := New(&cfg, dial, nil)
	cfg.Health.URL = server.URL + "/redirect"
	redirect := checker.Check(context.Background())
	if redirected || redirect.StatusCode != http.StatusFound || redirect.Status != "健康" {
		t.Fatalf("不应跟随重定向：已跳转=%v，结果=%+v", redirected, redirect)
	}
	cfg.Health.URL = server.URL + "/failure"
	failure := checker.Check(context.Background())
	if failure.Status != "异常" || failure.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("服务器错误不应记为健康：%+v", failure)
	}
}

func TestCheckUpstreamDialFailureIsUnhealthy(t *testing.T) {
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 1
	cfg.Health.StateFile = filepath.Join(t.TempDir(), "health.json")
	checker := New(&cfg, func(context.Context, string, string) (net.Conn, error) {
		return nil, os.ErrPermission
	}, nil)
	result := checker.Check(context.Background())
	if result.Status != "异常" || result.Error == "" {
		t.Fatalf("上游拨号失败应记为异常：%+v", result)
	}
}

func TestCheckRejectsOverflowingTimeoutWithoutPanic(t *testing.T) {
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = config.MaxHealthSeconds + 1
	cfg.Health.StateFile = filepath.Join(t.TempDir(), "health.json")
	checker := New(&cfg, func(context.Context, string, string) (net.Conn, error) {
		return nil, os.ErrPermission
	}, nil)
	result := checker.Check(context.Background())
	if result.Status != "异常" || result.Error != "健康检测超时配置无效" {
		t.Fatalf("无效超时配置应返回明确错误：%+v", result)
	}
}

func TestCheckDoesNotExposeURLAndRejectsIncompleteBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "短响应")
		w.(http.Flusher).Flush()
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 2
	cfg.Health.StateFile = filepath.Join(t.TempDir(), "health.json")
	cfg.Health.URL = server.URL + "/probe?secret=不得泄漏"
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	result := New(&cfg, dial, nil).Check(context.Background())
	if result.Status != "异常" {
		t.Fatalf("响应正文读取不完整时不能记为健康：%+v", result)
	}
	if strings.Contains(result.Error, "不得泄漏") || strings.Contains(result.Error, "secret") {
		t.Fatalf("错误消息泄漏了检测地址参数：%q", result.Error)
	}
}
