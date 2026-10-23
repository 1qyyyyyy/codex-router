package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-router/config"
)

func TestProbeMeasuresWithoutOverwritingHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Health.URL = server.URL
	cfg.Health.StateFile = filepath.Join(t.TempDir(), "health.json")
	previous := []byte("保留周期健康结果")
	if err := os.WriteFile(cfg.Health.StateFile, previous, 0600); err != nil {
		t.Fatal(err)
	}
	dialed := false
	checker := New(&cfg, func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = true
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}, nil)
	result := checker.Probe(context.Background())
	if !dialed || result.Status != "健康" || result.LatencyMS < 15 {
		t.Fatalf("应通过指定拨号器测量请求耗时：%+v", result)
	}
	data, err := os.ReadFile(cfg.Health.StateFile)
	if err != nil || string(data) != string(previous) {
		t.Fatalf("实时测量不应覆盖周期健康状态：%s，%v", data, err)
	}
}

func TestLatencyMonitorRefreshAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := &LatencyMonitor{}
	if !monitor.Snapshot().Time.IsZero() {
		t.Fatal("首次采样前应没有结果")
	}
	called := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		monitor.Run(ctx, func(ctx context.Context) Result {
			called <- struct{}{}
			return Result{Time: time.Now(), Status: "健康", LatencyMS: 25}
		})
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-called:
		case <-time.After(7 * time.Second):
			t.Fatal("应立即采样并在五秒后更新")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("停止时应及时退出")
	}
	if result := monitor.Snapshot(); result.Status != "健康" || result.LatencyMS != 25 {
		t.Fatalf("应保存有效采样结果：%+v", result)
	}
}

func TestLatencyMonitorTimeoutAndInFlightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := &LatencyMonitor{}
	started := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		monitor.Run(ctx, func(ctx context.Context) Result {
			started <- struct{}{}
			now := time.Now()
			<-ctx.Done()
			return Result{Time: now, Status: "已取消", LatencyMS: time.Since(now).Milliseconds()}
		})
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(7 * time.Second):
			t.Fatal("首次超时后应继续监测")
		}
	}
	result := monitor.Snapshot()
	if result.Status != "异常" || result.Error != "延迟检测超时" {
		t.Fatalf("检测超时不应冒充有效延迟：%+v", result)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("停止时应取消正在进行的检测")
	}
	if monitor.Snapshot() != result {
		t.Fatal("主动停止不应覆盖最后一次结果")
	}
}
