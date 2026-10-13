package proxy

import (
	"codex-router/config"
	"net/http/httptest"
	"testing"
)

func TestObservationsBoundedAndExcludeChecks(t *testing.T) {
	cfg := config.Default()
	s, err := New(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := "api.openai.com"
	if !cfg.MatchTarget(host) {
		t.Fatal("测试目标必须匹配路由")
	}
	s.observe(host, "443", "测试节点", true, true)
	s.observe("example.org", "443", "直连", true, false)
	if len(s.RecentTargets()) != 0 {
		t.Fatal("检测和直连不应记录")
	}
	for i := 0; i < 40; i++ {
		s.observe(host, "443", "测试节点", i%2 == 0, false)
	}
	targets := s.RecentTargets()
	if len(targets) != 32 {
		t.Fatal("记录应有界")
	}
	targets[0].Host = "已修改"
	if s.RecentTargets()[0].Host == "已修改" {
		t.Fatal("快照应独立复制")
	}
	req := httptest.NewRequest("CONNECT", "http://api.openai.com:443", nil)
	req.Header.Set(InternalProbeHeader, s.InternalProbeToken())
	if !s.internalRequest(req) || req.Header.Get(InternalProbeHeader) != "" {
		t.Fatal("内部检测标记必须识别并剥离")
	}
	req.Header.Set(InternalProbeHeader, "伪造标记")
	if s.internalRequest(req) || req.Header.Get(InternalProbeHeader) != "" {
		t.Fatal("伪造标记不能排除应用请求且必须剥离")
	}
}
