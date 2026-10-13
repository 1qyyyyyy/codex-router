//go:build windows

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackgroundStateRejectsRemoteAndMalformedControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, state := range []backgroundState{
		{PID: 1, Address: "example.com:80", Token: strings.Repeat("a", 64)},
		{PID: 1, Address: "127.0.0.1:1234", Token: "短令牌"},
		{PID: 0, Address: "127.0.0.1:1234", Token: strings.Repeat("a", 64)},
	} {
		data, _ := json.Marshal(state)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBackground(path); err == nil {
			t.Fatal("应拒绝无效后台状态")
		}
	}
}

func TestBackgroundControlChecksIdentityAndStopMethod(t *testing.T) {
	token := strings.Repeat("a", 64)
	method := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method = request.Method
		if request.URL.Path == "/stop" && request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("缺少控制令牌")
		}
		_, _ = writer.Write([]byte(token))
	}))
	defer server.Close()
	state := backgroundState{Address: strings.TrimPrefix(server.URL, "http://"), Token: token}
	if err := contactBackground(state, "stop"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Fatal("停止请求必须使用提交方法")
	}
	state.Token = strings.Repeat("b", 64)
	if err := contactBackground(state, "status"); err == nil {
		t.Fatal("应拒绝身份不一致的控制响应")
	}
}
