package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// 存储 key 固定，避免调用方拼出任意路径。
const (
	KeyDefault         = "config-default"
	KeyUser            = "config-user"
	keyPrefixWorkspace = "config-workspace-"
	keyPrefixProfile   = "config-profile-"
)

// CompileRequest 描述一次有效配置编译请求。
//
// SessionOverrides 是会话级覆盖（最高优先层）；它不是落盘配置，
// 只影响本次编译结果——会话结束后不残留。
type CompileRequest struct {
	WorkspaceID      contracts.WorkspaceID
	ProfileID        contracts.ProfileID
	SessionOverrides json.RawMessage
}

// WorkspaceKey / ProfileKey 给出各层在存储中的 key。
func WorkspaceKey(id contracts.WorkspaceID) string {
	return keyPrefixWorkspace + sanitizeKeyPart(string(id))
}

func ProfileKey(id contracts.ProfileID) string {
	return keyPrefixProfile + sanitizeKeyPart(string(id))
}

// sanitizeKeyPart 把领域 ID 转成可安全用作 key 的形式。
// 非法字符一律替换，保证不产生路径分隔符。
func sanitizeKeyPart(s string) string {
	if strings.TrimSpace(s) == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Compile 按五层合并产出有效配置（CFG01 / CFG03）。
//
// 返回的 AgentDir 是本产品独立的 PI_CODING_AGENT_DIR：始终位于配置存储根目录下，
// 绝不指向用户真实 ~/.pi/agent（CFG04、基线 §5）。
func (s *Store) Compile(ctx context.Context, req CompileRequest) (contracts.EffectiveConfig, error) {
	specs := []struct {
		name LayerName
		key  string
	}{
		{LayerDefault, KeyDefault},
		{LayerUser, KeyUser},
		{LayerWorkspace, WorkspaceKey(req.WorkspaceID)},
		{LayerProfile, ProfileKey(req.ProfileID)},
	}
	layers := make([]Layer, 0, 5)
	var maxRevision uint64
	for _, spec := range specs {
		doc, err := s.Load(ctx, spec.key)
		if err != nil {
			return contracts.EffectiveConfig{}, err
		}
		if doc.Revision > maxRevision {
			maxRevision = doc.Revision
		}
		if len(doc.Body) > 0 {
			layers = append(layers, Layer{Name: spec.name, Body: doc.Body})
		}
	}
	if len(req.SessionOverrides) > 0 {
		// 会话覆盖同样要经过 same schema 校验，不能绕过 schemaVersion 约定。
		normalized, err := normalizeBody(req.SessionOverrides)
		if err != nil {
			return contracts.EffectiveConfig{}, err
		}
		layers = append(layers, Layer{Name: LayerSession, Body: normalized})
	}

	merged, err := Merge(layers)
	if err != nil {
		return contracts.EffectiveConfig{}, err
	}

	valuesJSON, err := json.Marshal(merged.Values)
	if err != nil {
		return contracts.EffectiveConfig{}, contracts.NewAppError(contracts.CodeInternal, "序列化有效配置失败: "+err.Error(), false)
	}
	sum := sha256.Sum256(valuesJSON)

	return contracts.EffectiveConfig{
		Revision:   maxRevision,
		Hash:       "sha256:" + hex.EncodeToString(sum[:]),
		Values:     json.RawMessage(valuesJSON),
		Provenance: merged.Provenance,
		AgentDir:   s.AgentDir(req.WorkspaceID, req.ProfileID),
		Warnings:   merged.Warnings,
	}, nil
}

// AgentDir 返回该 workspace/profile 组合的独立 agent 目录。
//
// 目录本身由调用方在启动 worker 前创建；这里只给出确定性路径，
// 保证不同 workspace 不共享 agent 状态，且不落在用户 ~/.pi 下。
func (s *Store) AgentDir(ws contracts.WorkspaceID, profile contracts.ProfileID) string {
	return filepath.Join(s.root, "agents", sanitizeKeyPart(string(ws))+"-"+sanitizeKeyPart(string(profile)))
}
