package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const InternalProbeHeader = "X-Codex-Router-Internal-Probe"

type Observation struct {
	Time    time.Time `json:"时间"`
	Host    string    `json:"目标主机"`
	Port    string    `json:"端口"`
	Node    string    `json:"节点"`
	Success bool      `json:"成功"`
}

type observations struct {
	mu    sync.Mutex
	items []Observation
	token string
}

func newObservations() *observations {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		panic("生成内部检测标记失败")
	}
	return &observations{token: hex.EncodeToString(token)}
}

func (s *Server) InternalProbeToken() string { return s.observations.token }
func (s *Server) RecentTargets() []Observation {
	o := s.observations
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Observation(nil), o.items...)
}
func (s *Server) internalRequest(r *http.Request) bool {
	internal := r.Header.Get(InternalProbeHeader) == s.observations.token
	r.Header.Del(InternalProbeHeader)
	return internal
}
func (s *Server) observe(host, port, node string, success, internal bool) {
	if internal || !s.cfg.MatchTarget(host) {
		return
	}
	o := s.observations
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.items) >= 32 {
		copy(o.items, o.items[1:])
		o.items = o.items[:31]
	}
	o.items = append(o.items, Observation{Time: time.Now(), Host: host, Port: port, Node: node, Success: success})
}
