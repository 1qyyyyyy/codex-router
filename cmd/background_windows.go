//go:build windows

package main

import (
	"codex-router/config"
	"codex-router/health"
	"codex-router/integration"
	"codex-router/proxy"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type backgroundState struct {
	PID     int    `json:"进程"`
	Address string `json:"控制地址"`
	Token   string `json:"令牌"`
	Proxy   string `json:"代理地址"`
}

func backgroundPaths(path string) (string, string) {
	base := filepath.Join(filepath.Dir(path), "state")
	return filepath.Join(base, "background.json"), filepath.Join(base, "proxy-backup.json")
}

func readBackground(path string) (backgroundState, error) {
	var state backgroundState
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	if err == nil {
		host, _, splitErr := net.SplitHostPort(state.Address)
		if splitErr != nil || host != "127.0.0.1" || len(state.Token) != 64 || state.PID <= 0 {
			err = fmt.Errorf("后台状态文件无效")
		}
	}
	return state, err
}

func contactBackground(state backgroundState, action string) error {
	method := http.MethodGet
	if action == "stop" {
		method = http.MethodPost
	}
	request, err := http.NewRequest(method, "http://"+state.Address+"/"+action, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.Token)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("后台控制请求失败：%d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		return err
	}
	if string(data) != state.Token {
		return fmt.Errorf("后台身份校验失败")
	}
	return nil
}

func diagnoseRouting(path string, out io.Writer) error {
	for _, step := range routingSteps(path) {
		fmt.Fprintf(out, "%s：%s %s 建议：%s\n", step.Name, step.Status, step.Detail, step.Advice)
	}
	return nil
}

func backgroundCommand(action, path string, out io.Writer) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	statePath, backupPath := backgroundPaths(absolute)
	if action == "background" {
		return backgroundWorker(absolute)
	}
	state, stateErr := readBackground(statePath)
	running := stateErr == nil && contactBackground(state, "status") == nil
	switch action {
	case "status":
		if !running {
			return fmt.Errorf("后台代理未运行；出口尚未接管")
		}
		fmt.Fprintln(out, desktopStatus(absolute))
		return nil
	case "stop":
		if running {
			if err := contactBackground(state, "stop"); err != nil {
				return err
			}
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
					fmt.Fprintln(out, "代理已结束，原系统代理和用户代理环境变量已恢复。请重启联网应用。")
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return fmt.Errorf("停止尚未完成，请检查日志和代理备份，不要强制删除备份")
		}
		release, err := lockBackground()
		if err != nil {
			return fmt.Errorf("后台进程仍在启动或停止中，请稍后重试：%w", err)
		}
		defer release()
		if _, err := os.Stat(backupPath); err == nil {
			if err := integration.RestoreSystemProxy(backupPath); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if stateErr == nil {
			_ = os.Remove(statePath)
		}
		fmt.Fprintln(out, "代理未运行；遗留代理设置已恢复。")
		return nil
	case "start":
		if running {
			fmt.Fprintln(out, "代理已经在后台运行，无需重复启动。")
			return nil
		}
		if _, err := os.Stat(backupPath); err == nil {
			return fmt.Errorf("存在未恢复的代理备份，请先运行结束代理")
		}
		cfg, err := config.Load(absolute)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		logPath := filepath.Join(filepath.Dir(absolute), "logs", "background.log")
		if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
			return err
		}
		// 此文件只保存本次启动失败信息，每次启动覆盖旧内容，避免历史错误累积。
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer logFile.Close()
		child := exec.Command(executable, "background", "--config", absolute)
		child.Dir = filepath.Dir(absolute)
		child.Stdout, child.Stderr = logFile, logFile
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
		if err := child.Start(); err != nil {
			return fmt.Errorf("启动后台代理失败：%w", err)
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		deadline := time.Now().Add(time.Duration(cfg.Health.TimeoutSeconds+15) * time.Second)
		for time.Now().Before(deadline) {
			state, err := readBackground(statePath)
			if err == nil && contactBackground(state, "status") == nil {
				fmt.Fprintf(out, "代理已在后台启动，入口：%s。可以关闭此窗口。\n请完全退出并重新打开桌面应用，让新代理设置生效。\n", state.Proxy)
				return nil
			}
			select {
			case <-done:
				return fmt.Errorf("后台启动失败，请查看 %s；上游异常时不会启用系统代理", logPath)
			default:
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("后台启动超时，请查看 %s，并用结束代理恢复设置", logPath)
	}
	return fmt.Errorf("未知后台操作")
}

func lockBackground() (func(), error) {
	runtime.LockOSThread()
	name, _ := windows.UTF16PtrFromString(`Local\CodexRouterUserProxy`)
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	wait, err := windows.WaitForSingleObject(mutex, 0)
	if err != nil || (wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED) {
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("已有后台代理正在管理系统设置")
	}
	return func() { windows.ReleaseMutex(mutex); windows.CloseHandle(mutex); runtime.UnlockOSThread() }, nil
}

func backgroundWorker(path string) (resultErr error) {
	release, err := lockBackground()
	if err != nil {
		return err
	}
	defer release()
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	logger, closeLog, err := newLoggerWithOutput(cfg.Log.File, cfg.Log.Level, io.Discard)
	if err != nil {
		return err
	}
	defer closeLog()
	upstream, err := proxy.NewUpstreamDialer(cfg)
	if err != nil {
		return err
	}
	check := health.New(cfg, upstream, logger).Check(context.Background())
	if check.Status != "健康" {
		return fmt.Errorf("上游不可用，未启用系统代理：%s", check.Error)
	}
	server, err := proxy.New(cfg, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress())
	if err != nil {
		return fmt.Errorf("本地代理端口不可用：%w", err)
	}
	defer listener.Close()
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer control.Close()
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	state := backgroundState{PID: os.Getpid(), Address: control.Addr().String(), Token: hex.EncodeToString(token), Proxy: cfg.ProxyURL()}
	statePath, backupPath := backgroundPaths(path)
	if err := integration.EnableSystemProxy(cfg.ProxyURL(), backupPath); err != nil {
		return err
	}
	defer func() {
		if err := integration.RestoreSystemProxy(backupPath); err != nil {
			resultErr = fmt.Errorf("恢复原代理设置失败：%w", err)
			logger.Error("代理设置恢复失败，备份已保留", "错误", err)
			return
		}
		_ = os.Remove(statePath)
	}()
	startedAt := time.Now()
	var healthMu sync.Mutex
	currentHealth := check
	latency := &health.LatencyMonitor{}
	var stopping atomic.Bool
	stopped := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+state.Token {
			http.Error(writer, "未授权", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/snapshot" {
			healthMu.Lock()
			result := currentHealth
			healthMu.Unlock()
			currentConfig, configErr := config.Load(path)
			configChanged := configErr != nil || !reflect.DeepEqual(cfg, currentConfig)
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(statusSnapshot{ConfigChanged: configChanged, Running: !stopping.Load(), Started: startedAt, Proxy: state.Proxy, Health: result, Latency: latency.Snapshot(), Fresh: !stopping.Load() && time.Since(result.Time) < time.Duration(cfg.Health.IntervalSeconds+cfg.Health.TimeoutSeconds+5)*time.Second, Targets: server.RecentTargets()})
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/probe" {
			_ = json.NewEncoder(writer).Encode(localProbe(cfg, server))
			return
		}
		stopRequest := request.URL.Path == "/stop" && request.Method == http.MethodPost
		if !stopRequest && (request.URL.Path != "/status" || request.Method != http.MethodGet) {
			http.NotFound(writer, request)
			return
		}
		_, _ = io.WriteString(writer, state.Token)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if stopRequest {
			stopping.Store(true)
			select {
			case stopped <- struct{}{}:
			default:
			}
		}
	})
	controlServer := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second}
	defer controlServer.Close()
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- server.Serve(listener) }()
	go func() { errorsCh <- controlServer.Serve(control) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := statePath + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, statePath); err != nil {
		return err
	}
	logger.Info("后台代理已启动并接管系统代理", "监听地址", cfg.ListenAddress(), "进程", os.Getpid())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	latencyDone := make(chan struct{})
	go func() {
		defer close(latencyDone)
		latency.Run(ctx, health.New(cfg, upstream, logger).Probe)
	}()
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		ticker := time.NewTicker(time.Duration(cfg.Health.IntervalSeconds) * time.Second)
		defer ticker.Stop()
		checker := health.New(cfg, upstream, logger)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result := checker.Check(ctx)
				if ctx.Err() != nil || result.Status == "已取消" {
					return
				}
				healthMu.Lock()
				currentHealth = result
				healthMu.Unlock()
			}
		}
	}()
	select {
	case <-stopped:
	case err := <-errorsCh:
		resultErr = err
	}
	cancel()
	<-healthDone
	<-latencyDone
	logger.Info("后台代理准备停止并恢复设置")
	return resultErr
}
