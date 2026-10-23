package health

import (
	"context"
	"sync"
	"time"
)

const LatencyInterval = 5 * time.Second
const LatencyTimeout = 5 * time.Second
const LatencyMaxAge = LatencyInterval + LatencyTimeout + 2*time.Second

// LatencyMonitor 串行测量上游延迟；快照只读内存，不等待网络。
type LatencyMonitor struct {
	mu     sync.RWMutex
	latest Result
}

func (m *LatencyMonitor) Snapshot() Result {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest
}

// Run 立即开始检测，之后每五秒检测，停止时取消正在进行的请求。
func (m *LatencyMonitor) Run(ctx context.Context, probe func(context.Context) Result) {
	ticker := time.NewTicker(LatencyInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		checkCtx, cancel := context.WithTimeout(ctx, LatencyTimeout)
		result := probe(checkCtx)
		if checkCtx.Err() != nil && ctx.Err() == nil {
			result.Status = "异常"
			result.Error = "延迟检测超时"
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.latest = result
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
