package app

// RES04 的外部观察：产品写进隔离 agentDir 的配置，从磁盘读回。
//
// 为什么不是把 setup.ModelsJSON 直接当证据：那是同一份代码在内存里的中间产物。
// 这里读的是 ensureRuntime 真正落到 <appData>/agents/.../models.json 的那份文件，
// 哈希、字节数、模型条数都从磁盘上的字节算出来。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// observeAgentConfig 读回 agentDir 里的 models.json，写 agent-config.json artifact，
// 返回从该文件解析出的事实。
func observeAgentConfig(t *testing.T, agentDir string) map[string]any {
	t.Helper()
	if appObservationDir() == "" {
		return nil
	}
	path := filepath.Join(agentDir, "models.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回产品写下的 %s 失败: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	writeAppObservation(t, map[string]any{}, map[string][]byte{"agent-config.json": raw})

	// 事实从磁盘字节重新解析，不复用写文件时的结构体。
	// Pi 的 models.json 里 providers 是**对象**（providerID → 配置），不是数组。
	var doc struct {
		Providers map[string]struct {
			BaseURL string            `json:"baseUrl"`
			Models  []json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("磁盘上的 models.json 不是合法 JSON: %v", err)
	}
	modelCount := 0
	providers := make([]string, 0, len(doc.Providers))
	for id, p := range doc.Providers {
		providers = append(providers, id)
		modelCount += len(p.Models)
	}
	sort.Strings(providers)
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatalf("列出 agentDir 失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return map[string]any{
		"agentConfigPath":     filepath.ToSlash(path),
		"agentConfigBytes":    len(raw),
		"agentConfigSha256":   "sha256:" + hex.EncodeToString(sum[:]),
		"agentConfigModels":   modelCount,
		"agentConfigProvider": providers,
		"agentDirEntries":     names,
	}
}
