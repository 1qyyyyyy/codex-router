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

func TestCheckCallerCancellationDoesNotPersist(t *testing.T) {
	t.Run("保留已有状态文件", func(t *testing.T) {
		statePath := filepath.Join(t.TempDir(), "health.json")
		previous := []byte(`{"状态":"健康","节点名":"旧节点"}`)
		if err := os.WriteFile(statePath, previous, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.Health.TimeoutSeconds = 5
		cfg.Health.StateFile = statePath
		started := make(chan struct{})
		checker := New(&cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan Result, 1)
		go func() { finished <- checker.Check(ctx) }()
		<-started
		cancel()
		result := <-finished
		if result.Status != "已取消" || result.Error != "健康检测已取消" {
			t.Fatalf("调用者取消应返回明确状态：%+v", result)
		}
		got, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(previous) {
			t.Fatalf("取消后已有状态文件被覆盖：%q", got)
		}
	})

	t.Run("不创建状态文件", func(t *testing.T) {
		statePath := filepath.Join(t.TempDir(), "missing", "health.json")
		cfg := config.Default()
		cfg.Health.TimeoutSeconds = 5
		cfg.Health.StateFile = statePath
		started := make(chan struct{})
		checker := New(&cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan Result, 1)
		go func() { finished <- checker.Check(ctx) }()
		<-started
		cancel()
		if result := <-finished; result.Status != "已取消" {
			t.Fatalf("调用者取消应返回已取消状态：%+v", result)
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatalf("取消时不应创建状态文件，实际错误：%v", err)
		}
	})
}

func TestCheckCallerDeadlineDoesNotPersist(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "health.json")
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 5
	cfg.Health.StateFile = statePath
	checker := New(&cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := checker.Check(ctx)
	if result.Status != "已取消" {
		t.Fatalf("调用者截止时间到期应返回已取消状态：%+v", result)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("调用者截止时间到期时不应创建状态文件，实际错误：%v", err)
	}
}

func TestCheckInternalTimeoutIsPersisted(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "health.json")
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 1
	cfg.Health.StateFile = statePath
	checker := New(&cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	result := checker.Check(context.Background())
	if result.Status != "异常" || result.Error == "" {
		t.Fatalf("检测器自身超时应作为异常返回：%+v", result)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("检测器自身超时应落盘：%v", err)
	}
	var saved Result
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("状态文件不是有效 JSON：%v", err)
	}
	if saved.Status != "异常" {
		t.Fatalf("内部超时保存状态错误：%+v", saved)
	}
}

func TestCheckAcceptsNilContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 2
	cfg.Health.StateFile = filepath.Join(t.TempDir(), "health.json")
	cfg.Health.URL = server.URL
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	result := New(&cfg, dial, nil).Check(nil)
	if result.Status != "健康" {
		t.Fatalf("nil 上下文应按后台上下文处理：%+v", result)
	}
}

func TestRunDoesNotCheckAfterCancellation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "health.json")
	cfg := config.Default()
	cfg.Health.TimeoutSeconds = 1
	cfg.Health.StateFile = statePath
	calls := 0
	checker := New(&cfg, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, os.ErrPermission
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker.Run(ctx)
	if calls != 0 {
		t.Fatalf("上下文取消后 Run 不应启动健康检查，调用次数：%d", calls)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("上下文取消后 Run 不应创建状态文件，实际错误：%v", err)
	}
}
