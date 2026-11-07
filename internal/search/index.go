// Package search 是 U01 的全局搜索：会话、Agent/Profile、Skill、产物的派生索引。
//
// 权威边界（规格 §5.1 / 基线 §5）：
//   - 索引是**派生**数据。sessions / artifacts 的权威在 SQLite 的各权威表里，
//     Agent/Profile 与 Skill 的权威在配置存储里；本包从不写它们，Rebuild 只读；
//   - 索引未建立时 Search 返回 unavailable，不返回空列表——"搜过了没有命中"
//     和"索引还没建"对用户是两件完全不同的事（SRCH01）；
//   - 检索使用 FTS5 trigram（与 C05 记忆检索同一约束）：中文按 3 字符滑窗切分，
//     少于 3 个字符的查询由本包拒绝（QUERY_TOO_SHORT），不降级成全表 LIKE；
//   - 结果带稳定 route id，由前端静态路由目录解析。路由形状是契约：
//     改它等于让已经存下来的诊断/书签指向别处，所以集中在这里一处生成。
package search

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// 可检索的类别。`agent` 就是规格里的 "Agent/Profile"：V1 的 Agent 是单个 Profile
// 的产品化编辑器，索引里用同一个 kind，避免同一份配置出现两条不同命名的结果。
const (
	KindSession  = "session"
	KindAgent    = "agent"
	KindSkill    = "skill"
	KindArtifact = "artifact"
)

// KindList 是全部类别，顺序即展示优先级（也是 ParseKinds 的返回值顺序）。
var KindList = []string{KindSession, KindAgent, KindSkill, KindArtifact}

// 索引可用性（与 binding 层、前端共用同一组字面量）。
const (
	AvailabilityAvailable   = "available"
	AvailabilityUnavailable = "unavailable"
)

const (
	// MinQueryRunes 是检索的最短长度（按 rune，不是字节）。
	MinQueryRunes = 3
	// DefaultLimit 是未指定 limit 时的返回条数。
	DefaultLimit = 20
	// MaxLimit 是单次检索的硬上限：再大就等于"一次拉全量"。
	MaxLimit = 100
)

// metaKey 是 search_meta 里的键。
const (
	metaBuiltAt   = "built_at"
	metaSessions  = "sessions"
	metaAgents    = "agents"
	metaSkills    = "skills"
	metaArtifacts = "artifacts"
	metaUnavail   = "unavailable"
)

// Doc 是一条待索引的文档。
//
// DocID 由 (Kind, RefID) 决定：重建是幂等的，重复重建不会产生第二份。
type Doc struct {
	Kind        string
	RefID       string
	WorkspaceID string
	Route       string
	Title       string
	Subtitle    string
	Body        string
	UpdatedAt   time.Time
}

// DocID 返回文档的稳定标识。
func (d Doc) DocID() string { return d.Kind + ":" + d.RefID }

// Item 是一条检索结果。
type Item struct {
	Kind        string    `json:"kind"`
	RefID       string    `json:"refId"`
	WorkspaceID string    `json:"workspaceId,omitempty"`
	Route       string    `json:"route"`
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Rank        float64   `json:"rank"`
}

// Query 是一次检索请求。
type Query struct {
	// Text 是用户输入；<3 rune 返回 QUERY_TOO_SHORT。
	Text string
	// Kinds 为空表示全部类别。
	Kinds []string
	// WorkspaceID 非空时只返回该工作区与 user scope（空 workspace_id）的条目。
	WorkspaceID string
	// Limit <=0 用 DefaultLimit，超过 MaxLimit 压到上限。
	Limit int
}

// Result 是一次检索的答案。
type Result struct {
	Query string `json:"query"`
	// Availability 为 unavailable 表示索引尚未建立；此时 Items 一定为空，
	// 调用方必须显示"不可用"，不能显示"0 条结果"。
	Availability string     `json:"availability"`
	IndexBuiltAt *time.Time `json:"indexBuiltAt,omitempty"`
	Items        []Item     `json:"items"`
}

// Summary 是一次重建的结果。
type Summary struct {
	BuiltAt   time.Time `json:"builtAt"`
	Sessions  int       `json:"sessions"`
	Agents    int       `json:"agents"`
	Skills    int       `json:"skills"`
	Artifacts int       `json:"artifacts"`
	// Unavailable 列出这次重建读不到的来源（例如 007 还没合入时没有 artifacts 表）。
	// 它们不会被静默当成"0 条"。
	Unavailable []string `json:"unavailable,omitempty"`
}

// Status 是索引状态（不触发重建）。
type Status struct {
	Availability string     `json:"availability"`
	BuiltAt      *time.Time `json:"builtAt,omitempty"`
	Summary      Summary    `json:"summary"`
}

// Source 是配置存储里的可检索来源（Agent/Profile 与 Skill）。
//
// 它们不在 SQLite 里，所以由装配层实现；本包只认"给我一组 Doc"。
type Source interface {
	Agents(ctx context.Context) ([]Doc, error)
	Skills(ctx context.Context) ([]Doc, error)
}

// RouteFor 返回某一类结果的稳定 route id。
//
// 与前端 `frontend/src/app/navigation.ts` 的 pattern 一一对应：
// session → /chat/:id，agent → /agents，skill → /skills，artifact → /library。
func RouteFor(kind, refID string) string {
	id := strings.TrimSpace(refID)
	switch kind {
	case KindSession:
		return "/chat/" + id
	case KindAgent:
		return "/agents?agent=" + id
	case KindSkill:
		return "/skills?skill=" + id
	case KindArtifact:
		return "/library?artifact=" + id
	default:
		return ""
	}
}

// ParseKinds 校验并归一化类别过滤条件。
//
// 接受空（=全部）与 `profile` 这个别名（规格写的是 Agent/Profile）；
// 未知类别返回 INVALID_INPUT，而不是静默忽略——静默忽略会让"我只想搜 Skill"
// 变成"什么都能搜到"。
func ParseKinds(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		k := strings.ToLower(strings.TrimSpace(r))
		if k == "profile" {
			k = KindAgent
		}
		if k == "" {
			continue
		}
		valid := false
		for _, known := range KindList {
			if k == known {
				valid = true
				break
			}
		}
		if !valid {
			return nil, contracts.NewAppError(contracts.CodeInvalidInput,
				"未知的搜索类别: "+r, false).
				WithParams(map[string]string{"field": "kinds", "value": r, "supported": strings.Join(KindList, ", ")})
		}
		seen[k] = true
	}
	// 按稳定顺序返回：过滤条件里的顺序不该改变 SQL 形状。
	ordered := make([]string, 0, len(seen))
	for _, known := range KindList {
		if seen[known] {
			ordered = append(ordered, known)
		}
	}
	return ordered, nil
}

// clampLimit 把 limit 收敛到 [1, MaxLimit]。
func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

// ftsPhrase 把用户输入变成一个 FTS5 短语查询。
//
// 整体加引号：输入里的 `-`、`*`、`(` 等是 FTS5 语法字符，不转义会变成语法错误
// 或意外的前缀查询；引号内的 `"` 用双写转义（与 C05 的记忆检索同一写法）。
func ftsPhrase(text string) string {
	return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
}

// runeCount 是查询长度的判定口径（按字符，不按字节）。
func runeCount(text string) int { return utf8.RuneCountInString(text) }

// parseTS 解析权威表里的固定宽度时间戳；解析不了就返回零值（只用它展示排序）。
func parseTS(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// baseName 取路径最后一段（索引里只是文本，不依赖平台文件系统语义）。
func baseName(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func atoiOr(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return fallback
	}
	return n
}

func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// isMissingTable 判断错误是否为"表不存在"。
//
// 它只用于一个判断：artifacts 的权威表还没被 007 建出来时，重建应该少收一类
// 而不是整体失败。其它读取错误仍然让重建失败（库坏了不能拿残缺索引冒充完整索引）。
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such table")
}
