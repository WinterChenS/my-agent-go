package app

// writeModels 的回归测试。
//
// 这里守的是一个真实踩过的坑：Pi 的 models.json schema 要求 apiKey 至少 1 个字符。
// 空密钥直写会让**整份** models.json 校验失败，Pi 于是报"provider 不存在"并退出，
// 前端只看到 RPC_WORKER_EXITED。症状与原因之间没有任何线索，
// 所以这个约束必须在写文件这一层就被满足，而不是等 Pi 报错。
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteModelsFillsApiKeyForLocalEndpoints(t *testing.T) {
	dir := t.TempDir()
	s := Settings{
		Provider: "fixture",
		BaseURL:  "http://127.0.0.1:1234/v1",
		Model:    "fixture-model",
		APIKey:   "", // 本机端点没有密钥——这是常态，不是错误
	}
	if err := writeModels(s, dir); err != nil {
		t.Fatalf("writeModels: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("读 models.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			APIKey  string `json:"apiKey"`
			BaseURL string `json:"baseUrl"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("models.json 不是合法 JSON: %v", err)
	}
	p, ok := doc.Providers["fixture"]
	if !ok {
		t.Fatalf("models.json 缺少 provider fixture：%s", raw)
	}
	if p.APIKey == "" {
		t.Fatal("apiKey 为空：Pi 会判定 models.json 非法，进而报“provider 不存在”")
	}
	if p.BaseURL != s.BaseURL {
		t.Fatalf("baseUrl 被改写: %q", p.BaseURL)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "fixture-model" {
		t.Fatalf("模型条目不对: %+v", p.Models)
	}
}

func TestWriteModelsKeepsProvidedApiKey(t *testing.T) {
	dir := t.TempDir()
	s := Settings{
		Provider: "fixture",
		BaseURL:  "https://api.example.test/v1",
		Model:    "m1",
		APIKey:   "sk-real",
	}
	if err := writeModels(s, dir); err != nil {
		t.Fatalf("writeModels: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("读 models.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("models.json 不是合法 JSON: %v", err)
	}
	if got := doc.Providers["fixture"].APIKey; got != "sk-real" {
		t.Fatalf("用户填的密钥不能被执行层的占位值覆盖，实际 %q", got)
	}
}

func TestWriteModelsRequiresEndpointAndModel(t *testing.T) {
	dir := t.TempDir()
	if err := writeModels(Settings{Model: "m"}, dir); err == nil {
		t.Fatal("缺 baseUrl 时应报配置错误")
	}
	if err := writeModels(Settings{BaseURL: "http://127.0.0.1:1/v1"}, dir); err == nil {
		t.Fatal("缺 model 时应报配置错误")
	}
}
