// diagnosis 包进行独立诊断，不写入健康状态。
package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codex-router/config"
	"codex-router/proxy"
)

type Step struct {
	Name       string `json:"名称"`
	Status     string `json:"状态"`
	Detail     string `json:"详情"`
	Advice     string `json:"建议"`
	DurationMS int64  `json:"耗时毫秒"`
}
type Report struct {
	Time    time.Time `json:"时间"`
	Steps   []Step    `json:"步骤"`
	Healthy bool      `json:"健康"`
}

func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "诊断时间：%s\n", r.Time.Format("2006-01-02 15:04:05"))
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "%s：%s（%d 毫秒） %s\n", s.Name, s.Status, s.DurationMS, s.Detail)
		if s.Advice != "" {
			fmt.Fprintf(&b, "建议：%s\n", s.Advice)
		}
	}
	return b.String()
}

func Check(ctx context.Context, cfg *config.Config) Report {
	r := Report{Time: time.Now()}
	for _, name := range []string{"上游端口", "协议", "认证", "目标连接"} {
		r.Steps = append(r.Steps, Step{Name: name, Status: "跳过", Detail: "前置步骤尚未通过"})
	}
	if ctx == nil {
		ctx = context.Background()
	}
	seconds := 10
	if cfg != nil && cfg.Health.TimeoutSeconds > 0 {
		seconds = cfg.Health.TimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	set := func(i int, status, detail, advice string, start time.Time) {
		r.Steps[i].Status = status
		r.Steps[i].Detail = detail
		r.Steps[i].Advice = advice
		r.Steps[i].DurationMS = time.Since(start).Milliseconds()
	}
	start := time.Now()
	if cfg == nil {
		set(0, "失败", "缺少代理配置", "加载有效配置后重试", start)
		return r
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", cfg.UpstreamAddress())
	if err != nil {
		set(0, "失败", "无法建立上游端口连接", "确认手机与电脑在同一网络、已开放局域网代理，并检查端口、防火墙和地址", start)
		return r
	}
	c.Close()
	set(0, "通过", "上游端口接受连接", "", start)
	u, err := url.Parse(cfg.Health.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		set(1, "失败", "检测目标配置无效，无法执行握手", "填写不含凭据的有效网页检测地址", time.Now())
		return r
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	start = time.Now()
	c, protocol, err := proxy.DiagnosticDial(ctx, cfg, net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		var e *proxy.DiagnosticError
		if errors.As(err, &e) && e.Stage != "协议" {
			set(1, "通过", "已识别"+protocol, "", start)
			if e.Stage == "认证" {
				set(2, "失败", "代理拒绝认证", "核对上游用户名和密码，并确认代理允许该认证方式", start)
			} else {
				if e.AuthenticationConfirmed {
					set(2, "通过", "代理接受当前认证方式", "", start)
				} else {
					set(2, "跳过", "代理未建立隧道，无法确认认证是否被接受", "检查代理访问规则及认证设置", start)
				}
				set(3, "失败", "代理无法建立目标隧道", "检查手机代理出口、目标解析和节点连通性，或更换检测目标", start)
			}
		} else {
			set(1, "失败", "上游未返回受支持的有效代理协议", "确认填写的是手机开放的代理入口，核对网页代理或第五版套接字代理类型", start)
		}
		return r
	}
	defer c.Close()
	set(1, "通过", "已识别"+protocol, "", start)
	set(2, "通过", "代理接受当前认证方式", "", start)
	start = time.Now()
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return c, nil }, ForceAttemptHTTP2: false}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		set(3, "失败", "无法创建检测请求", "检查检测目标配置", start)
		return r
	}
	resp, err := client.Do(req)
	if err != nil {
		set(3, "失败", "目标网页请求或加密握手失败", "检查目标证书、手机代理出口和目标服务，必要时更换检测地址", start)
		return r
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == 407 {
		set(3, "失败", fmt.Sprintf("目标返回状态码 %d", resp.StatusCode), "检查目标服务或隧道后的中间代理；此响应不是上游隧道认证结果", start)
		return r
	}
	set(3, "通过", fmt.Sprintf("目标返回状态码 %d；仅证明网络连接，目标权限状态不代表代理认证失败", resp.StatusCode), "", start)
	r.Healthy = true
	return r
}
