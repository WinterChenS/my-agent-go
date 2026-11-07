package connectors

// 连接器的审计日志。
//
// 追加型：只有 Append 与"按连接器读回"，没有任何更新/删除入口。审计不是
// 展示数据，它是"这次外发到底是谁批的"的唯一凭证，可改就等于没有。
//
// 每行只出现 credential **ref**：凭证材料不进审计（CONN03 的 sentinel 扫描
// 就打在 audit 行上）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AuditEntry 是一条审计记录。
type AuditEntry struct {
	Seq int64 `json:"seq,omitempty"`
	// At 是 UTC 时间；写进 SQLite 与 JSONL 都用 RFC3339Nano。
	At          time.Time `json:"at"`
	ConnectorID string    `json:"connectorId"`
	ActionID    string    `json:"actionId"`
	Kind        Kind      `json:"kind"`
	// Decision 是策略结论（allow/deny/ask）或 "n/a"（未进入策略判定）。
	Decision string `json:"decision"`
	// Outcome 是结果类别或流程节点：allow/deny/rate_limited/blocked/
	// done/failed/outcome_unknown/approval_required/approval_timeout。
	Outcome        string `json:"outcome"`
	Reason         string `json:"reason,omitempty"`
	CredentialRef  string `json:"credentialRef,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	ApprovalID     string `json:"approvalId,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	RunID          string `json:"runId,omitempty"`
	ToolName       string `json:"toolName,omitempty"`
	// Detail 是给人看的一句话（驱动返回的说明或拒绝原因）。
	Detail string `json:"detail,omitempty"`
}

// AuditStore 是审计的写入/读回接口。
type AuditStore interface {
	Append(ctx context.Context, e AuditEntry) error
	// ListAudits 按连接器读回审计行；与 DefinitionStore.List 分开命名，
	// 因为同一个 SQLStore 同时实现两者（同名方法在 Go 里不能共存）。
	ListAudits(ctx context.Context, connectorID string, limit int) ([]AuditEntry, error)
}

// MultiAudit 把同一条审计分发给多个 sink（SQLite + 观察目录）。
//
// 主 sink 失败即整体失败：观察文件写不进去不能让"审计已经落库"变成假象；
// 反过来主 sink 成功、镜像失败时仍然报错——两条路径应当同进同退，
// 由调用方决定是否可以继续（当前实现按失败处理，宁可显式报错）。
type MultiAudit struct {
	sinks []AuditStore
}

// Sinks 返回组合的 sink 数量（诊断用）。
func (m *MultiAudit) Sinks() int { return len(m.sinks) }

// NewMultiAudit 组合多个审计 sink。
func NewMultiAudit(sinks ...AuditStore) (*MultiAudit, error) {
	out := make([]AuditStore, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("connectors: 审计 sink 为空")
	}
	return &MultiAudit{sinks: out}, nil
}

// Append 依次写入每个 sink。
func (m *MultiAudit) Append(ctx context.Context, e AuditEntry) error {
	for _, s := range m.sinks {
		if err := s.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// ListAudits 从第一个 sink 读回（其它 sink 是镜像）。
func (m *MultiAudit) ListAudits(ctx context.Context, connectorID string, limit int) ([]AuditEntry, error) {
	return m.sinks[0].ListAudits(ctx, connectorID, limit)
}

// JSONLAudit 是"一行一条 JSON"的审计文件。
//
// 存在理由：外部 oracle 要能从测试进程之外读事实。SQLite 也能读，但观察目录
// 里的 audit.jsonl 不需要知道 schema，runner 直接 grep/解析即可。
type JSONLAudit struct {
	mu   sync.Mutex
	path string
}

// NewJSONLAudit 打开（必要时创建）一个 JSONL 审计文件。
func NewJSONLAudit(path string) (*JSONLAudit, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("connectors: 审计文件路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("connectors: 创建审计目录失败: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("connectors: 打开审计文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("connectors: 关闭审计文件失败: %w", err)
	}
	return &JSONLAudit{path: path}, nil
}

// Path 返回审计文件路径。
func (j *JSONLAudit) Path() string { return j.path }

// Append 追加一行。
func (j *JSONLAudit) Append(ctx context.Context, e AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("connectors: 序列化审计失败: %w", err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("connectors: 打开审计文件失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("connectors: 写审计失败: %w", err)
	}
	return nil
}

// ListAudits 读回审计行（按 connectorID 过滤，limit<=0 表示全部）。
func (j *JSONLAudit) ListAudits(ctx context.Context, connectorID string, limit int) ([]AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	data, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("connectors: 读审计文件失败: %w", err)
	}
	out := []AuditEntry{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("connectors: 审计文件含非法行: %w", err)
		}
		if connectorID != "" && e.ConnectorID != connectorID {
			continue
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
