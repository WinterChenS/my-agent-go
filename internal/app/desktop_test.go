package app

// 旧设置路径（provider/baseUrl/model/apiKey 四个字段）的回归测试。
//
// 这里守两个真实踩过的坑：
//
//  1. Pi 的 models.json schema 要求 apiKey 至少 1 个字符。空密钥直写会让
//     **整份** models.json 校验失败，Pi 于是报"provider 不存在"并退出，
//     前端只看到 RPC_WORKER_EXITED。症状与原因之间没有任何线索。
//  2. C01 的做法是把明文密钥写进 models.json。SEC07 要求配置/日志/诊断包对
//     密钥的命中数为 0——"旧路径"不是豁免，它同样只写 $ENV 引用。
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/resources"
)

func writeLegacyModels(t *testing.T, dir string, s Settings) (string, []string) {
	t.Helper()
	setup, err := legacyModelSetup(s)
	if err != nil {
		t.Fatalf("legacyModelSetup: %v", err)
	}
	if err := resources.WritePiModels(dir, resources.PiModels{JSON: setup.ModelsJSON}); err != nil {
		t.Fatalf("写 models.json: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("读 models.json: %v", err)
	}
	return string(raw), setup.Env
}

func TestLegacyModelsFillsApiKeyForLocalEndpoints(t *testing.T) {
	dir := t.TempDir()
	raw, env := writeLegacyModels(t, dir, Settings{
		Provider: "fixture",
		BaseURL:  "http://127.0.0.1:1234/v1",
		Model:    "fixture-model",
		APIKey:   "", // 本机端点没有密钥——这是常态，不是错误
	})

	var doc struct {
		Providers map[string]struct {
			APIKey  string `json:"apiKey"`
			BaseURL string `json:"baseUrl"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("models.json 不是合法 JSON: %v", err)
	}
	p, ok := doc.Providers["fixture"]
	if !ok {
		t.Fatalf("models.json 缺少 provider fixture：%s", raw)
	}
	if p.APIKey == "" {
		t.Fatal("apiKey 为空：Pi 会判定 models.json 非法，进而报“provider 不存在”")
	}
	if p.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("baseUrl 被改写: %q", p.BaseURL)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "fixture-model" {
		t.Fatalf("模型条目不对: %+v", p.Models)
	}
	if len(env) != 0 {
		t.Fatalf("没有密钥就不该注入凭证环境变量，实际 %v", env)
	}
}

func TestLegacyModelsKeepsSecretOutOfJSON(t *testing.T) {
	dir := t.TempDir()
	const secret = "sk-real-do-not-persist"
	raw, env := writeLegacyModels(t, dir, Settings{
		Provider: "fixture",
		BaseURL:  "https://api.example.test/v1",
		Model:    "m1",
		APIKey:   secret,
	})

	if strings.Contains(raw, secret) {
		t.Fatalf("明文密钥出现在 models.json 里：\n%s", raw)
	}
	var doc struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("models.json 不是合法 JSON: %v", err)
	}
	if got := doc.Providers["fixture"].APIKey; got != "$"+resources.CredentialEnvName("settings-api-key") {
		t.Fatalf("apiKey 应是指向环境变量的引用，实际 %q", got)
	}
	if len(env) != 1 || env[0] != resources.CredentialEnvName("settings-api-key")+"="+secret {
		t.Fatalf("密钥应经环境变量交付，实际 %v", env)
	}
}

func TestLegacyModelsRequiresEndpointAndModel(t *testing.T) {
	if _, err := legacyModelSetup(Settings{Model: "m"}); err == nil {
		t.Fatal("缺 baseUrl 时应报配置错误")
	}
	if _, err := legacyModelSetup(Settings{BaseURL: "http://127.0.0.1:1/v1"}); err == nil {
		t.Fatal("缺 model 时应报配置错误")
	}
}
