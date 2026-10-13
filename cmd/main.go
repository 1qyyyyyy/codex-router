package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"codex-router/config"
	"codex-router/health"
	"codex-router/integration"
	"codex-router/proxy"
	"codex-router/service"
)

const version = "1.2.1"

func main() {
	if err := execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func execute(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runService("")
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printHelp(stdout)
		return nil
	}
	if hasHelpFlag(args[1:]) {
		printHelp(stdout)
		return nil
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-version" {
		fmt.Fprintf(stdout, "版本：%s\n", version)
		return nil
	}
	switch args[0] {
	case "start", "stop", "status", "background":
		path, err := parseRequiredConfigFlag(args[1:], args[0])
		if err != nil {
			return err
		}
		return backgroundCommand(args[0], path, stdout)
	case "tray", "panel", "diagnose":
		path, err := parseConfigFlag(args[1:], args[0])
		if err != nil {
			return err
		}
		path, err = chooseConfigPath(path)
		if err != nil {
			return err
		}
		if args[0] == "diagnose" {
			return runDiagnosis(path, stdout)
		}
		return runDesktop(path)
	case "run":
		path, err := parseConfigFlag(args[1:], "run")
		if err != nil {
			return err
		}
		return runService(path)
	case "init":
		path, err := parseConfigFlag(args[1:], "init")
		if err != nil {
			return err
		}
		path, err = chooseConfigPath(path)
		if err != nil {
			return err
		}
		if err := config.WriteDefault(path); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "默认配置已就绪：%s\n", path)
		return nil
	case "check":
		path, err := parseRequiredConfigFlag(args[1:], "check")
		if err != nil {
			return err
		}
		if _, err := config.Load(path); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "配置有效。")
		return nil
	case "health":
		path, err := parseRequiredConfigFlag(args[1:], "health")
		if err != nil {
			return err
		}
		return runHealth(path, stdout)
	case "launch":
		path, app, appArgs, err := parseLaunchArgs(args[1:])
		if err != nil {
			return err
		}
		return launch(path, app, appArgs)
	case "service":
		if len(args) < 2 {
			return fmt.Errorf("service 命令需要 install、uninstall、start、stop 或 status 操作")
		}
		action := args[1]
		path, err := parseRequiredConfigFlag(args[2:], "service")
		if err != nil {
			return err
		}
		if action == "install" {
			if _, err := config.Load(path); err != nil {
				return err
			}
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("解析配置绝对路径失败：%w", err)
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("获取程序路径失败：%w", err)
		}
		return service.Manage(action, executable, absolute)
	case "system-proxy":
		if len(args) < 2 {
			return fmt.Errorf("system-proxy 命令需要 enable 或 restore 操作")
		}
		action := args[1]
		path, err := parseRequiredConfigFlag(args[2:], "system-proxy")
		if err != nil {
			return err
		}
		return systemProxy(action, path)
	default:
		return fmt.Errorf("未知命令 %q；使用 --help 查看帮助", args[0])
	}
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `本地路由器

用法：codex-router [命令] [选项]

命令：
  start|stop|status --config 路径
                              启动后台代理、恢复设置并结束代理、检查运行状态
  tray|panel [--config 路径]  打开桌面控制面板
  diagnose [--config 路径]    分步诊断上游、本地入口及系统代理
  run                         前台运行本地代理服务（默认命令）
  init [--config 路径]        生成默认配置，不覆盖已有文件
  check --config 路径         校验配置
  health --config 路径        通过上游检测一次并输出 JSON
  launch --config 路径 [--app 可执行文件] [-- 应用参数]
                              启动代理并以该代理启动桌面应用
  service install|uninstall|start|stop|status --config 路径
                              管理系统服务
  system-proxy enable|restore --config 路径
                              启用或恢复系统代理
  version                     显示版本

选项必须放在子命令或服务操作之后。配置默认优先使用当前目录的 config.yaml，
其次使用程序所在目录的 config.yaml。桌面应用可使用 --app 指定可执行文件。
`)
}

func parseConfigFlag(args []string, command string) (string, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("解析%s命令选项失败：%w", command, err)
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("%s 命令不接受额外参数", command)
	}
	return *path, nil
}

func parseRequiredConfigFlag(args []string, command string) (string, error) {
	path, err := parseConfigFlag(args, command)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s 命令必须指定 --config 配置文件路径", command)
	}
	return path, nil
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func parseLaunchArgs(args []string) (string, string, []string, error) {
	options := args
	var appArgs []string
	for i, arg := range args {
		if arg == "--" {
			options, appArgs = args[:i], args[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "配置文件路径")
	app := fs.String("app", "", "应用可执行文件")
	if err := fs.Parse(options); err != nil {
		return "", "", nil, fmt.Errorf("解析 launch 命令选项失败：%w", err)
	}
	if fs.NArg() != 0 {
		return "", "", nil, fmt.Errorf("launch 命令的应用参数必须放在 -- 之后")
	}
	if strings.TrimSpace(*path) == "" {
		return "", "", nil, fmt.Errorf("launch 命令必须指定 --config 配置文件路径")
	}
	return *path, *app, appArgs, nil
}

func chooseConfigPath(path string) (string, error) {
	if path != "" {
		return filepath.Abs(path)
	}
	current, err := filepath.Abs("config.yaml")
	if err != nil {
		return "", fmt.Errorf("解析当前目录配置路径失败：%w", err)
	}
	if _, err := os.Stat(current); err == nil {
		return current, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("检查当前目录配置失败：%w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取程序路径失败：%w", err)
	}
	beside := filepath.Join(filepath.Dir(executable), "config.yaml")
	if _, err := os.Stat(beside); err == nil {
		return beside, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("检查程序目录配置失败：%w", err)
	}
	return current, nil
}

func runService(path string) error {
	resolved, err := chooseConfigPath(path)
	if err != nil {
		return err
	}
	return service.Run(func(ctx context.Context) error {
		return serve(ctx, resolved)
	})
}

func serve(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	logger, closeLog, err := newLogger(cfg.Log.File, cfg.Log.Level)
	if err != nil {
		return err
	}
	defer closeLog()
	server, err := proxy.New(cfg, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress())
	if err != nil {
		if errors.Is(err, os.ErrExist) || isAddressInUse(err) {
			return fmt.Errorf("无法监听 %s：端口已被占用。请使用已启动的代理，或关闭占用该端口的程序后重试：%w", cfg.ListenAddress(), err)
		}
		return fmt.Errorf("绑定监听地址 %s 失败：%w", cfg.ListenAddress(), err)
	}
	logger.Info("本地代理已启动", "监听地址", cfg.ListenAddress())
	upstreamDial, err := proxy.NewUpstreamDialer(cfg)
	if err != nil {
		_ = listener.Close()
		return err
	}
	checker := health.New(cfg, upstreamDial, logger)
	healthCtx, stopHealth := context.WithCancel(ctx)
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		checker.Run(healthCtx)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	var runErr error
	serveStopped := false
	select {
	case err := <-serveErr:
		serveStopped = true
		if err != nil {
			runErr = fmt.Errorf("代理服务异常退出：%w", err)
		}
	case <-ctx.Done():
	}
	stopHealth()
	<-healthDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("优雅停止代理失败：%w", err)
	}
	if !serveStopped {
		if err := <-serveErr; err != nil && runErr == nil {
			runErr = fmt.Errorf("代理停止时发生错误：%w", err)
		}
	}
	if runErr == nil {
		logger.Info("本地代理已停止")
	}
	return runErr
}

func isAddressInUse(err error) bool {
	var opErr *net.OpError
	return errors.Is(err, syscall.EADDRINUSE) || (errors.As(err, &opErr) && strings.Contains(strings.ToLower(opErr.Err.Error()), "address already in use"))
}

func newLogger(path, configuredLevel string) (*slog.Logger, func(), error) {
	return newLoggerWithOutput(path, configuredLevel, os.Stdout)
}

func newLoggerWithOutput(path, configuredLevel string, console io.Writer) (*slog.Logger, func(), error) {
	if path == "" {
		return nil, func() {}, fmt.Errorf("日志文件路径为空")
	}
	level, err := parseLogLevel(configuredLevel)
	if err != nil {
		return nil, func() {}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, func() {}, fmt.Errorf("创建日志目录失败：%w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, func() {}, fmt.Errorf("打开日志文件失败：%w", err)
	}
	options := &slog.HandlerOptions{Level: level, ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
		switch attr.Key {
		case slog.TimeKey:
			return slog.String("时间", attr.Value.Time().Format("2006-01-02 15:04:05"))
		case slog.LevelKey:
			level := "信息"
			switch attr.Value.Any().(slog.Level) {
			case slog.LevelDebug:
				level = "调试"
			case slog.LevelWarn:
				level = "警告"
			case slog.LevelError:
				level = "错误"
			}
			return slog.String("级别", level)
		case slog.MessageKey:
			return slog.String("消息", attr.Value.String())
		default:
			return attr
		}
	}}
	return slog.New(slog.NewTextHandler(io.MultiWriter(console, file), options)), func() { _ = file.Close() }, nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("日志级别只允许 debug、info、warn 或 error")
	}
}

func runHealth(path string, out io.Writer) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	dial, err := proxy.NewUpstreamDialer(cfg)
	if err != nil {
		return err
	}
	logger, closeLog, err := newLoggerWithOutput(cfg.Log.File, cfg.Log.Level, os.Stderr)
	if err != nil {
		return err
	}
	defer closeLog()
	result := health.New(cfg, dial, logger).Check(context.Background())
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("输出健康检测结果失败：%w", err)
	}
	if result.Status != "健康" {
		connection, err := net.DialTimeout("tcp", cfg.UpstreamAddress(), 2*time.Second)
		if err != nil {
			return fmt.Errorf("健康检测失败：上游入口 %s 无法建立网络连接；请确认实际地址、监听端口及手机共享代理已开启", cfg.UpstreamAddress())
		}
		_ = connection.Close()
		return fmt.Errorf("健康检测失败：%s", result.Error)
	}
	return diagnoseRouting(path, out)
}

func launch(path, app string, args []string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	logger, closeLog, err := newLogger(cfg.Log.File, cfg.Log.Level)
	if err != nil {
		return err
	}
	defer closeLog()
	server, err := proxy.New(cfg, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress())
	if err != nil {
		if isAddressInUse(err) {
			return fmt.Errorf("无法启动应用：本地代理端口 %s 已被占用。请使用已启动的代理，或关闭占用该端口的程序后重试：%w", cfg.ListenAddress(), err)
		}
		return fmt.Errorf("启动本地代理失败：绑定 %s：%w", cfg.ListenAddress(), err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	upstreamDial, err := proxy.NewUpstreamDialer(cfg)
	if err != nil {
		cancel()
		_ = listener.Close()
		return err
	}
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		health.New(cfg, upstreamDial, logger).Run(ctx)
	}()
	launchResult := make(chan error, 1)
	go func() { launchResult <- integration.Launch(ctx, cfg, app, args) }()
	var launchErr error
	serveStopped := false
	select {
	case launchErr = <-launchResult:
	case err := <-serveErr:
		serveStopped = true
		cancel()
		if err != nil {
			launchErr = fmt.Errorf("本地代理意外退出：%w", err)
		} else {
			launchErr = fmt.Errorf("本地代理意外停止")
		}
		<-launchResult
	case <-ctx.Done():
		cancel()
		<-launchResult
	}
	cancel()
	<-healthDone
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	shutdownErr := server.Shutdown(shutdownCtx)
	var serveResult error
	if !serveStopped {
		serveResult = <-serveErr
	}
	if launchErr != nil {
		return launchErr
	}
	if shutdownErr != nil {
		return fmt.Errorf("关闭本地代理失败：%w", shutdownErr)
	}
	if serveResult != nil {
		return fmt.Errorf("本地代理异常退出：%w", serveResult)
	}
	return nil
}

func systemProxy(action, path string) error {
	if action != "enable" && action != "restore" {
		return fmt.Errorf("system-proxy 操作只允许 enable 或 restore")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析配置绝对路径失败：%w", err)
	}
	backup := filepath.Join(filepath.Dir(absolute), "state", "proxy-backup.json")
	if action == "restore" {
		return integration.RestoreSystemProxy(backup)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	return integration.EnableSystemProxy(cfg.ProxyURL(), backup)
}
