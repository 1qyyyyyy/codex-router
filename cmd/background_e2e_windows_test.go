//go:build windows

package main

import (
	"bufio"
	"codex-router/diagnosis"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"codex-router/config"
	"codex-router/integration"
	"golang.org/x/sys/windows/registry"
	"gopkg.in/yaml.v3"
)

func currentProxyRegistry(t *testing.T) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for path, names := range map[string][]string{
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`: {"ProxyEnable", "ProxyServer", "ProxyOverride", "AutoConfigURL"},
		"Environment": {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"},
	} {
		key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			size, kind, err := key.GetValue(name, nil)
			if err == registry.ErrNotExist {
				values[path+name] = "不存在"
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			data := make([]byte, size)
			_, _, err = key.GetValue(name, data)
			if err != nil {
				t.Fatal(err)
			}
			values[path+name] = fmt.Sprintf("%d:%x", kind, data)
		}
		key.Close()
	}
	return values
}

func TestBackgroundLifecycleRealWindows(t *testing.T) {
	executable := os.Getenv("ROUTER_TEST_BINARY")
	if executable == "" {
		t.Skip("未开启会短暂修改并恢复系统代理的实机测试")
	}
	before := currentProxyRegistry(t)
	var connections atomic.Int32
	var applicationRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Test-Application") == "真实应用验证" {
			applicationRequests.Add(1)
		}
		_, _ = io.WriteString(writer, "经过上游")
	}))
	defer origin.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			http.Error(writer, "需要隧道", 400)
			return
		}
		connections.Add(1)
		destination, err := net.Dial("tcp", strings.TrimPrefix(origin.URL, "http://"))
		if err != nil {
			t.Error(err)
			return
		}
		client, buffered, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			destination.Close()
			t.Error(err)
			return
		}
		_, _ = io.WriteString(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(destination, buffered); destination.Close(); close(done) }()
		_, _ = io.Copy(client, destination)
		client.Close()
		<-done
	}))
	defer upstream.Close()
	directory := filepath.Join(t.TempDir(), "中文 带空格目录")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CodexRouter.exe", "启动代理.cmd", "检测出口.cmd", "结束代理.cmd"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(executable), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(name, ".cmd") {
			if strings.HasPrefix(string(data), "\xef\xbb\xbf") || strings.Contains(strings.ReplaceAll(string(data), "\r\n", ""), "\n") {
				t.Fatal("批处理编码或换行不符合发布要求")
			}
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable = filepath.Join(directory, "CodexRouter.exe")
	cfg := config.Default()
	upstreamURL, _ := url.Parse(upstream.URL)
	cfg.Upstream.Type, cfg.Upstream.Host = "http", upstreamURL.Hostname()
	cfg.Upstream.Port, _ = strconv.Atoi(upstreamURL.Port())
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Port = free.Addr().(*net.TCPAddr).Port
	free.Close()
	cfg.Health.URL = "http://chatgpt.com/路由验证"
	cfg.Health.TimeoutSeconds = 3
	path := filepath.Join(directory, "config.yaml")
	data, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(action string) ([]byte, error) {
		command := exec.Command(executable, action, "--config", path)
		return command.CombinedOutput()
	}
	t.Cleanup(func() {
		output, err := run("stop")
		if err != nil {
			t.Errorf("清理后台失败：%s，%v", output, err)
		}
	})
	batch := func(name string) *exec.Cmd {
		command := exec.Command("cmd.exe")
		command.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c ""` + filepath.Join(directory, name) + `""`, HideWindow: true}
		command.Stdin = strings.NewReader("\r\n")
		return command
	}
	parent := batch("启动代理.cmd")
	output, err := parent.CombinedOutput()
	if err != nil {
		t.Fatalf("后台启动失败：%s，%v", output, err)
	}
	if err := integration.VerifySystemProxy(cfg.ProxyURL()); err != nil {
		t.Fatal(err)
	}
	statePath, backupPath := backgroundPaths(path)
	state, err := readBackground(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := run("start"); err != nil || !strings.Contains(string(output), "已经") {
		t.Fatalf("重复启动不幂等：%s，%v", output, err)
	}
	second, _ := readBackground(statePath)
	if state.PID != second.PID {
		t.Fatal("重复启动生成了新后台")
	}
	time.Sleep(300 * time.Millisecond)
	if output, err := run("status"); err != nil {
		t.Fatalf("父命令窗口退出后后台失效：%s，%v", output, err)
	}
	var snapshot statusSnapshot
	if err := fetchControl(state, "snapshot", &snapshot); err != nil || len(snapshot.Targets) != 0 || !snapshot.Fresh {
		t.Fatalf("启动状态快照异常：%+v，%v", snapshot, err)
	}
	fake := state
	fake.Token = strings.Repeat("0", 64)
	if fetchControl(fake, "snapshot", &snapshot) == nil {
		t.Fatal("无效令牌不应读取快照")
	}
	var probe diagnosis.Step
	if err := fetchControl(state, "probe", &probe); err != nil || probe.Status != "通过" {
		t.Fatalf("内部入口检测失败：%+v，%v", probe, err)
	}
	if err := fetchControl(state, "snapshot", &snapshot); err != nil || len(snapshot.Targets) != 0 {
		t.Fatal("入口检测被错误记录为应用")
	}
	beforeRequests := applicationRequests.Load()
	proxyURL, _ := url.Parse(cfg.ProxyURL())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	applicationRequest, err := http.NewRequest(http.MethodGet, cfg.Health.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	applicationRequest.Header.Set("X-Test-Application", "真实应用验证")
	response, err := client.Do(applicationRequest)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	transport.CloseIdleConnections()
	if err != nil || string(body) != "经过上游" || applicationRequests.Load() != beforeRequests+1 || connections.Load() == 0 {
		t.Fatalf("未确认真实应用经上游到达源站：响应=%s，源站应用请求=%d，上游隧道=%d，错误=%v", body, applicationRequests.Load(), connections.Load(), err)
	}
	if err := fetchControl(state, "snapshot", &snapshot); err != nil || len(snapshot.Targets) != 1 || !snapshot.Targets[0].Success {
		t.Fatalf("真实应用请求未记录：%+v，%v", snapshot, err)
	}
	tunnel, err := net.DialTimeout("tcp", cfg.ListenAddress(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(tunnel, "CONNECT chatgpt.com:443 HTTP/1.1\r\nHost: chatgpt.com:443\r\n\r\n")
	reader := bufio.NewReader(tunnel)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		tunnel.Close()
		t.Fatalf("应用隧道失败：%s，%v", line, err)
	}
	_, _ = reader.ReadString('\n')
	if err := fetchControl(state, "snapshot", &snapshot); err != nil || len(snapshot.Targets) != 2 || snapshot.Targets[1].Port != "443" {
		tunnel.Close()
		t.Fatalf("活动隧道未及时记录：%+v，%v", snapshot, err)
	}
	tunnel.Close()
	if output, err := run("diagnose"); err != nil || !strings.Contains(string(output), "系统代理：通过") {
		t.Fatalf("分步诊断失败：%s，%v", output, err)
	}
	if err := fetchControl(state, "snapshot", &snapshot); err != nil || len(snapshot.Targets) != 2 {
		t.Fatal("分步诊断被记录为应用")
	}
	if output, err := run("health"); err != nil || !strings.Contains(string(output), "本地入口") {
		t.Fatalf("双层检测失败：%s，%v", output, err)
	}
	if output, err := batch("检测出口.cmd").CombinedOutput(); err != nil {
		t.Fatalf("检测批处理失败：%s，%v", output, err)
	}
	if output, err := batch("结束代理.cmd").CombinedOutput(); err != nil {
		t.Fatalf("结束失败：%s，%v", output, err)
	}
	if strings.Contains(desktopStatus(path), "上游健康：健康") {
		t.Fatal("停止后显示旧健康为当前健康")
	}
	if after := currentProxyRegistry(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("设置未精确恢复：原值=%v，现值=%v", before, after)
	}
	for _, file := range []string{statePath, backupPath} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("退出后遗留状态：%s", file)
		}
	}
	connection, err := net.DialTimeout("tcp", cfg.ListenAddress(), 300*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("停止后端口仍在监听")
	}
	if output, err := run("stop"); err != nil {
		t.Fatalf("重复结束失败：%s，%v", output, err)
	}
	if output, err := run("start"); err != nil {
		t.Fatalf("再次启动失败：%s，%v", output, err)
	}
	state, _ = readBackground(statePath)
	process, err := os.FindProcess(state.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if output, err := run("stop"); err != nil {
		t.Fatalf("崩溃恢复失败：%s，%v", output, err)
	}
	if strings.Contains(desktopStatus(path), "上游健康：健康") {
		t.Fatal("停止后显示旧健康为当前健康")
	}
	if after := currentProxyRegistry(t); !reflect.DeepEqual(before, after) {
		t.Fatal("异常终止后未精确恢复设置")
	}
	upstream.Close()
	if output, err := run("start"); err == nil {
		t.Fatalf("不可用上游不应报告启动成功：%s", output)
	}
	if strings.Contains(desktopStatus(path), "上游健康：健康") {
		t.Fatal("停止后显示旧健康为当前健康")
	}
	if after := currentProxyRegistry(t); !reflect.DeepEqual(before, after) {
		t.Fatal("不可用上游改变了系统设置")
	}
	t.Log("三个真实批处理、中文带空格目录、父命令结束后转发、重复启动、双层检测、停止释放端口及崩溃恢复均通过")
}
