package config

import "testing"

func TestDefaultTargetsCoverRootsAndSubdomains(t *testing.T) {
	cfg := Default()
	for _, domain := range []string{"api.openai.com", "auth.openai.com", "openai.com", "chatgpt.com", "ws.chatgpt.com", "oaistatic.com", "cdn.oaistatic.com", "oaiusercontent.com", "files.oaiusercontent.com"} {
		if !cfg.MatchTarget(domain) {
			t.Errorf("默认目标未覆盖：%s", domain)
		}
	}
	for _, domain := range []string{"api.openai.com.example.com", "notchatgpt.com", "example.com"} {
		if cfg.MatchTarget(domain) {
			t.Errorf("默认目标错误接管：%s", domain)
		}
	}
}
