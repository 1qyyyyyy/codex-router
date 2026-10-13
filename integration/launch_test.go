package integration

import (
	"runtime"
	"strings"
	"testing"
)

func TestProxyEnvironmentAndMerge(t *testing.T) {
	proxy := "http://127.0.0.1:17890"
	updates := proxyEnvironment(proxy)
	merged := mergeEnvironment([]string{
		"HTTP_PROXY=http://old-upper",
		"http_proxy=http://old-lower",
		"HTTPS_PROXY=http://old-secure",
		"KEEP=value",
	}, updates)
	values := make(map[string][]string)
	for _, item := range merged {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			values[key] = append(values[key], value)
		}
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
			if got := values[key]; len(got) != 1 || (key != "NO_PROXY" && got[0] != proxy) {
				t.Fatalf("Windows 环境变量 %s 合并错误：%v", key, got)
			}
		}
	} else {
		for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
			if got := values[key]; len(got) != 1 {
				t.Fatalf("Unix 环境变量 %s 应只出现一次，实际：%v", key, got)
			}
		}
		if values["HTTP_PROXY"][0] != proxy || values["http_proxy"][0] != proxy {
			t.Fatalf("大、小写代理环境变量值不一致：%v", values)
		}
	}
	if values["KEEP"][0] != "value" {
		t.Fatalf("无关环境变量被修改：%v", values["KEEP"])
	}
}

func TestRejectProxyArguments(t *testing.T) {
	for _, arg := range []string{"--proxy-server=http://other:8000", "--proxy-server", "--no-proxy-server", "--no-proxy-server=localhost"} {
		if err := rejectProxyArguments([]string{"--new-window", arg}); err == nil {
			t.Errorf("应拒绝冲突参数 %q", arg)
		}
	}
	if err := rejectProxyArguments([]string{"--new-window", "--profile-directory=Default"}); err != nil {
		t.Fatalf("误拒绝普通参数：%v", err)
	}
}

func TestElectronProxyArguments(t *testing.T) {
	got := electronProxyArgs("http://127.0.0.1:17890")
	if len(got) != 2 || got[0] != "--proxy-server=http://127.0.0.1:17890" || !strings.Contains(got[1], "127.0.0.1") {
		t.Fatalf("Electron 代理参数不正确：%v", got)
	}
}
