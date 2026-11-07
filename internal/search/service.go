// 搜索服务：签名与实现（U01 / SRCH01）。
//
// 一个 Service 只做三件事：Rebuild（从权威表重建）、Search（过滤 → 排名 → 限流）、
// Status（回答"索引建过没有"）。它不缓存、不增量、不在写入路径上挂钩子：
// 索引是派生数据，唯一会让它变化的是显式 Rebuild，这个决定让"重建"成为
// 可观察、可重复的动作，而不是散落在业务写入里的隐式行为。
package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// Service 是全局搜索服务。并发安全：状态全在 SQLite 里。
type Service struct {
	db  *storage.DB
	src Source
	now func() time.Time
}

// New 构造搜索服务。db 必须是已经迁移到最新 schema 的索引库（<appRoot>/sessions.db）。
func New(db *storage.DB, src Source) (*Service, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "搜索索引缺少数据库句柄", false)
	}
	return &Service{db: db, src: src, now: time.Now}, nil
}

// SetClock 注入时钟，仅用于测试确定性。
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Status 读索引状态（不重建、不写库）。
func (s *Service) Status(ctx context.Context) (Status, error) {
	meta, err := s.readMeta(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{Availability: AvailabilityUnavailable, Summary: Summary{Unavailable: splitList(meta[metaUnavail])}}
	builtAt := strings.TrimSpace(meta[metaBuiltAt])
	if builtAt == "" {
		return st, nil
	}
	ts, perr := time.Parse(time.RFC3339Nano, builtAt)
	if perr != nil {
		return Status{}, contracts.NewAppError(contracts.CodeInternal,
			"搜索索引时间戳无法解析（疑似被外部改写）: "+perr.Error(), false)
	}
	st.Availability = AvailabilityAvailable
	st.BuiltAt = &ts
	st.Summary = Summary{
		BuiltAt:     ts,
		Sessions:    atoiOr(meta[metaSessions], 0),
		Agents:      atoiOr(meta[metaAgents], 0),
		Skills:      atoiOr(meta[metaSkills], 0),
		Artifacts:   atoiOr(meta[metaArtifacts], 0),
		Unavailable: splitList(meta[metaUnavail]),
	}
	return st, nil
}

// Search 执行一次检索。
//
// 顺序是固定的：先校验查询长度，再读索引状态，最后才 MATCH。
// 反过来做会让"索引没建"掩盖"查询太短"，用户看到的是"不可用"而不是
// "再多输一个字"。
func (s *Service) Search(ctx context.Context, q Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	text := strings.TrimSpace(q.Text)
	res := Result{Query: text, Availability: AvailabilityUnavailable, Items: []Item{}}
	if n := runeCount(text); n < MinQueryRunes {
		return Result{}, contracts.NewAppError(contracts.CodeQueryTooShort,
			fmt.Sprintf("检索关键词至少 %d 个字符（当前 %d 个）；请输入更长的关键词", MinQueryRunes, n),
			false).
			WithParams(map[string]string{"min": fmt.Sprint(MinQueryRunes), "count": fmt.Sprint(n)})
	}
	kinds, err := ParseKinds(q.Kinds)
	if err != nil {
		return Result{}, err
	}

	st, err := s.Status(ctx)
	if err != nil {
		return Result{}, err
	}
	if st.Availability != AvailabilityAvailable {
		return res, nil
	}
	res.Availability = AvailabilityAvailable
	res.IndexBuiltAt = st.BuiltAt
	items, err := s.runQuery(ctx, text, kinds, q.WorkspaceID, clampLimit(q.Limit))
	if err != nil {
		return Result{}, err
	}
	if items == nil {
		items = []Item{}
	}
	res.Items = items
	return res, nil
}

// runQuery 执行 MATCH + 过滤 + 排名。
func (s *Service) runQuery(ctx context.Context, text string, kinds []string, workspaceID string, limit int) ([]Item, error) {
	where := []string{`search_fts MATCH ?`}
	args := []any{ftsPhrase(text)}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, k := range kinds {
			placeholders[i] = "?"
			args = append(args, k)
		}
		where = append(where, `kind IN (`+strings.Join(placeholders, ", ")+`)`)
	}
	if ws := strings.TrimSpace(workspaceID); ws != "" {
		// user scope（workspace_id 为空）对哪个工作区都可见；其余必须同项目。
		where = append(where, `(workspace_id = '' OR workspace_id = ?)`)
		args = append(args, ws)
	}
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT kind, ref_id, workspace_id, route, title, subtitle, updated_at, bm25(search_fts) AS rank
		  FROM search_fts
		 WHERE `+strings.Join(where, ` AND `)+`
		 ORDER BY rank, kind, ref_id
		 LIMIT ?`, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "搜索失败: "+err.Error(), true)
	}
	defer rows.Close()

	out := make([]Item, 0, limit)
	for rows.Next() {
		var (
			item    Item
			updated string
		)
		if err := rows.Scan(&item.Kind, &item.RefID, &item.WorkspaceID, &item.Route,
			&item.Title, &item.Subtitle, &updated, &item.Rank); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取搜索结果失败: "+err.Error(), false)
		}
		item.UpdatedAt = parseTS(updated)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "搜索结果异常: "+err.Error(), true)
	}
	return out, nil
}

// Rebuild 从权威来源重建索引。
//
// 一个事务里先清空再写入：中途失败不会留下"一半新一半旧"的可检索状态。
// 它不修改任何权威表，因此可以随时重跑（重启后重建是 SRCH01 的要求）。
func (s *Service) Rebuild(ctx context.Context) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	docs, unavailable, err := s.collect(ctx)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{BuiltAt: s.now().UTC(), Unavailable: unavailable}
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, contracts.NewAppError(contracts.CodeInternal, "开启索引事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM search_fts`); err != nil {
		return Summary{}, contracts.NewAppError(contracts.CodeInternal, "清空搜索索引失败: "+err.Error(), true)
	}
	for _, d := range docs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_fts(title, subtitle, body, doc_id, kind, ref_id, workspace_id, route, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Title, d.Subtitle, d.Body, d.DocID(), d.Kind, d.RefID, d.WorkspaceID,
			d.Route, d.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return Summary{}, contracts.NewAppError(contracts.CodeInternal, "写入搜索索引失败: "+err.Error(), true)
		}
		switch d.Kind {
		case KindSession:
			summary.Sessions++
		case KindAgent:
			summary.Agents++
		case KindSkill:
			summary.Skills++
		case KindArtifact:
			summary.Artifacts++
		}
	}

	meta := map[string]string{
		metaBuiltAt:   summary.BuiltAt.Format(time.RFC3339Nano),
		metaSessions:  fmt.Sprint(summary.Sessions),
		metaAgents:    fmt.Sprint(summary.Agents),
		metaSkills:    fmt.Sprint(summary.Skills),
		metaArtifacts: fmt.Sprint(summary.Artifacts),
		metaUnavail:   strings.Join(unavailable, ","),
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO search_meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return Summary{}, contracts.NewAppError(contracts.CodeInternal, "写入索引状态失败: "+err.Error(), true)
		}
	}
	if err := tx.Commit(); err != nil {
		return Summary{}, contracts.NewAppError(contracts.CodeInternal, "提交索引事务失败: "+err.Error(), true)
	}
	return summary, nil
}

// collect 读取全部权威来源。
//
// 某个来源的权威表还不存在（例如 007 未合入时没有 artifacts 表）不算失败：
// 记进 Unavailable 并继续，因为"少一类可检索项"比"整次重建失败"更接近事实。
// 其它读取错误必须让重建失败——那说明库坏了，不能拿一份残缺索引冒充完整索引。
func (s *Service) collect(ctx context.Context) ([]Doc, []string, error) {
	var (
		docs        []Doc
		unavailable []string
	)

	sessions, err := s.collectSessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	docs = append(docs, sessions...)

	artifacts, err := s.collectArtifacts(ctx)
	if err != nil {
		if !isMissingTable(err) {
			return nil, nil, err
		}
		unavailable = append(unavailable, KindArtifact)
	} else {
		docs = append(docs, artifacts...)
	}

	if s.src != nil {
		agents, err := s.src.Agents(ctx)
		if err != nil {
			return nil, nil, err
		}
		docs = append(docs, agents...)
		skills, err := s.src.Skills(ctx)
		if err != nil {
			return nil, nil, err
		}
		docs = append(docs, skills...)
	}
	return docs, unavailable, nil
}

// collectSessions 读普通会话（未删除、未归档、kind=chat）。
//
// 与 C06 目录同一口径：归档/删除/调度会话不进列表，也就不进检索结果。
func (s *Service) collectSessions(ctx context.Context) ([]Doc, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, workspace_id, title, updated_at
		  FROM sessions
		 WHERE deleted_at = '' AND archived = 0 AND kind = 'chat'`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取会话索引失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]Doc, 0, 32)
	for rows.Next() {
		var id, workspace, title, updated string
		if err := rows.Scan(&id, &workspace, &title, &updated); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描会话索引失败: "+err.Error(), false)
		}
		title = strings.TrimSpace(title)
		// 没有标题的会话不进入索引：目录里显示的是本地化占位文案，
		// 那份占位不是内容，索引它只会让"未命名会话"成为高频命中。
		if title == "" {
			continue
		}
		out = append(out, Doc{
			Kind:        KindSession,
			RefID:       id,
			WorkspaceID: workspace,
			Route:       RouteFor(KindSession, id),
			Title:       title,
			UpdatedAt:   parseTS(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "会话索引异常: "+err.Error(), false)
	}
	return out, nil
}

// collectArtifacts 读产物索引。
func (s *Service) collectArtifacts(ctx context.Context) ([]Doc, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, workspace_id, display_name, canonical_path, updated_at
		  FROM artifacts`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取产物索引失败: "+err.Error(), false)
	}
	defer rows.Close()

	out := make([]Doc, 0, 32)
	for rows.Next() {
		var id, workspace, display, path, updated string
		if err := rows.Scan(&id, &workspace, &display, &path, &updated); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描产物索引失败: "+err.Error(), false)
		}
		title := strings.TrimSpace(display)
		if title == "" {
			title = baseName(path)
		}
		if title == "" {
			continue
		}
		out = append(out, Doc{
			Kind:        KindArtifact,
			RefID:       id,
			WorkspaceID: workspace,
			Route:       RouteFor(KindArtifact, id),
			Title:       title,
			Subtitle:    strings.TrimSpace(path),
			// 路径也进正文：用户经常记得"那个 report.md"，不记得显示名。
			Body:      strings.TrimSpace(path),
			UpdatedAt: parseTS(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "产物索引异常: "+err.Error(), false)
	}
	return out, nil
}

// readMeta 一次读出 search_meta（键值表很小）。
func (s *Service) readMeta(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT key, value FROM search_meta`)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取索引状态失败: "+err.Error(), false)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描索引状态失败: "+err.Error(), false)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "索引状态异常: "+err.Error(), false)
	}
	return out, nil
}
