package main

import (
	"codex-router/config"
	"gopkg.in/yaml.v3"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosisCLIExitHelper(t *testing.T) {
	path := os.Getenv("ROUTER_DIAGNOSIS_EXIT_TEST")
	if path == "" {
		return
	}
	os.Args = []string{os.Args[0], "diagnose", "--config", path}
	main()
}

func TestDiagnosisFailureReturnsExitAndCompleteReport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg := config.Default()
	cfg.Upstream.Host = "127.0.0.1"
	cfg.Upstream.Port = port
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Port = local.Addr().(*net.TCPAddr).Port
	local.Close()
	cfg.Health.TimeoutSeconds = 1
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDiagnosisCLIExitHelper$")
	command.Env = append(os.Environ(), "ROUTER_DIAGNOSIS_EXIT_TEST="+path)
	output, err := command.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("故障诊断应退出为错误：%s，%v", output, err)
	}
	for _, name := range []string{"上游端口", "协议", "认证", "目标连接", "本地入口", "系统代理"} {
		if !strings.Contains(string(output), name+"：") {
			t.Fatalf("缺少完整诊断步骤%s：%s", name, output)
		}
	}
	if !strings.Contains(string(output), "诊断发现故障") {
		t.Fatalf("缺少明确故障总结：%s", output)
	}
}
