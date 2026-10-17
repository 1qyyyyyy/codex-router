package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"codex-router/config"
)

const (
	maxActiveRequests     = 256
	responseHeaderTimeout = 2 * time.Minute
)

type Server struct {
	observations     *observations
	cfg              *config.Config
	logger           *slog.Logger
	upstream         func(context.Context, string, string) (net.Conn, error)
	transport        *http.Transport
	httpServer       *http.Server
	mu               sync.Mutex
	active           map[net.Conn]struct{}
	closed           bool
	tunnelWG         sync.WaitGroup
	ctx              context.Context
	cancel           context.CancelFunc
	detectedProtocol func() string
	protocolMu       sync.Mutex
	loggedProtocol   string
	requestSlots     chan struct{}
}

// New 校验配置并创建本地代理服务。
func New(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("代理配置不能为空")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	up, detected, err := NewUpstreamDialerWithProtocol(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{observations: newObservations(), cfg: cfg, logger: logger, upstream: up, active: make(map[net.Conn]struct{}), ctx: ctx, cancel: cancel, detectedProtocol: detected, requestSlots: make(chan struct{}, maxActiveRequests)}
	// 只限制响应头等待和连接池规模；正文及已建立的隧道保留持续流式传输能力。
	s.transport = &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, DisableKeepAlives: false, ResponseHeaderTimeout: responseHeaderTimeout,
		DialContext: s.dial, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
		MaxIdleConns: 64, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}
	s.httpServer = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	return s, nil
}

// Handler 返回 HTTP 代理处理器。
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.handle) }

// Serve 在给定监听器上运行服务。
func (s *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return fmt.Errorf("监听器不能为空")
	}
	if !isLoopbackAddr(listener.Addr()) {
		_ = listener.Close()
		return fmt.Errorf("代理监听器必须绑定环回地址")
	}
	err := s.httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown 停止接收请求并关闭所有活动隧道。
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	s.closed = true
	for c := range s.active {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.cancel()
	s.transport.CloseIdleConnections()
	err := s.httpServer.Shutdown(ctx)
	if err != nil {
		_ = s.httpServer.Close()
	}
	done := make(chan struct{})
	go func() { s.tunnelWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}

func isLoopbackAddr(a net.Addr) bool {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) add(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = c.Close()
		return false
	}
	s.active[c] = struct{}{}
	return true
}
func (s *Server) remove(c net.Conn) { s.mu.Lock(); delete(s.active, c); s.mu.Unlock(); _ = c.Close() }

// acquireRequest 对普通请求和整个隧道生命周期共用额度，超限时立即返回而不排队。
func (s *Server) acquireRequest(w http.ResponseWriter) bool {
	if s.ctx.Err() != nil {
		http.Error(w, "代理正在停止", http.StatusServiceUnavailable)
		return false
	}
	select {
	case s.requestSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "代理活动连接已达上限，请稍后重试", http.StatusServiceUnavailable)
		return false
	}
}

func (s *Server) releaseRequest() { <-s.requestSlots }

func (s *Server) startTunnel(client net.Conn, buffered io.Reader, upstream net.Conn, host, node string, started time.Time) bool {
	s.mu.Lock()
	if s.closed {
		delete(s.active, client)
		delete(s.active, upstream)
		s.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return false
	}
	s.tunnelWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.tunnelWG.Done()
		defer s.releaseRequest()
		s.tunnel(client, buffered, upstream, host, node, started)
	}()
	return true
}

func (s *Server) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("目标地址无效")
	}
	if s.cfg.MatchTarget(host) {
		return s.upstream(ctx, network, address)
	}
	return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil {
		http.Error(w, "代理请求无效", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	if !s.acquireRequest(w) {
		return
	}
	defer s.releaseRequest()
	target := r.URL
	if target.Host == "" {
		if r.Host == "" {
			http.Error(w, "代理请求缺少目标主机", http.StatusBadRequest)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL = &url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
		target = clone.URL
		r = clone
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		http.Error(w, "代理仅支持 HTTP 和 HTTPS 目标", http.StatusBadRequest)
		return
	}
	internal := s.internalRequest(r)
	host := target.Hostname()
	port := target.Port()
	if port == "" {
		port = "80"
		if target.Scheme == "https" {
			port = "443"
		}
	}
	started := time.Now()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL = cloneURL(target)
	out.Host = target.Host
	stripHopHeaders(out.Header)
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Authenticate")
	resp, err := s.transport.RoundTrip(out)
	s.observe(host, port, s.nodeFor(host), err == nil, internal)
	if err != nil {
		node := s.nodeFor(host)
		s.logger.Warn("代理请求失败", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusBadGateway)
		http.Error(w, "代理连接失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	node := s.nodeFor(host)
	stripHopHeaders(resp.Header)
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, copyErr := io.Copy(flushWriter{w}, resp.Body)
	status := any(resp.StatusCode)
	message := "HTTP 请求完成"
	if copyErr != nil {
		status = "响应中断"
		message = "HTTP 响应中断"
	}
	s.logger.Info(message, "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", status)
}

func (s *Server) nodeFor(host string) string {
	if !s.cfg.MatchTarget(host) {
		return "直连"
	}
	protocol := s.detectedProtocol()
	if strings.EqualFold(s.cfg.Upstream.Type, "auto") && protocol != "" {
		s.protocolMu.Lock()
		if s.loggedProtocol != protocol {
			s.loggedProtocol = protocol
			s.logger.Info("已识别上游协议", "目标", host, "节点", protocol)
		}
		s.protocolMu.Unlock()
	}
	if protocol != "" && strings.EqualFold(s.cfg.Upstream.Type, "auto") {
		return protocol
	}
	if s.cfg.Upstream.Name != "" {
		return s.cfg.Upstream.Name
	}
	return "上游代理"
}

type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

func cloneURL(u *url.URL) *url.URL { v := *u; return &v }
func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHopHeaders(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !s.acquireRequest(w) {
		return
	}
	releaseInHandler := true
	defer func() {
		if releaseInHandler {
			s.releaseRequest()
		}
	}()
	internal := s.internalRequest(r)
	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	if err := validateProxyTarget(target); err != nil {
		http.Error(w, "CONNECT 目标地址无效", http.StatusBadRequest)
		return
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		http.Error(w, "CONNECT 目标地址无效", http.StatusBadRequest)
		return
	}
	started := time.Now()
	node := s.nodeFor(host)
	client, bw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "无法建立隧道", http.StatusInternalServerError)
		return
	}
	if !s.add(client) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	up, err := s.dial(ctx, "tcp", target)
	cancel()
	node = s.nodeFor(host)
	if err != nil {
		s.observe(host, port, node, false, internal)
	}
	if err != nil {
		_, _ = io.WriteString(bw, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		_ = bw.Flush()
		s.logger.Warn("CONNECT 失败", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusBadGateway)
		s.remove(client)
		return
	}
	if !s.add(up) {
		s.logger.Warn("CONNECT 失败", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusBadGateway)
		s.remove(client)
		return
	}
	if _, err = bw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err == nil {
		err = bw.Flush()
	}
	if err != nil {
		s.logger.Warn("CONNECT 响应失败", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusBadGateway)
		s.remove(up)
		s.remove(client)
		return
	}
	s.observe(host, port, node, true, internal)
	s.logger.Info("CONNECT 已建立", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusOK)
	if !s.startTunnel(client, bw.Reader, up, host, node, started) {
		s.logger.Warn("CONNECT 隧道启动失败", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusBadGateway)
		return
	}
	// 隧道脱离请求处理器后，由隧道协程在实际关闭连接时归还额度。
	releaseInHandler = false
}

func (s *Server) tunnel(client net.Conn, buffered io.Reader, upstream net.Conn, host, node string, started time.Time) {
	defer s.remove(client)
	defer s.remove(upstream)
	defer func() {
		s.logger.Info("CONNECT 隧道已关闭", "目标", host, "节点", node, "延迟毫秒", time.Since(started).Milliseconds(), "状态", http.StatusOK)
	}()
	done := make(chan error, 2)
	go func() { _, e := io.Copy(upstream, buffered); closeWrite(upstream); done <- e }()
	go func() { _, e := io.Copy(client, upstream); closeWrite(client); done <- e }()
	first := <-done
	if first != nil {
		_ = client.Close()
		_ = upstream.Close()
	}
	<-done
}

func closeWrite(c net.Conn) {
	if v, ok := c.(interface{ CloseWrite() error }); ok {
		_ = v.CloseWrite()
	}
}
