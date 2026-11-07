package app

// U01 的搜索绑定层（SRCH01）：全局搜索与索引重建。
//
// 三条纪律：
//   - 业务判断（查询长度、kind 过滤、scope 过滤、排名、索引状态）全在
//     internal/search，这里不重写一遍，否则两个地方的说法迟早不一致；
//   - 索引库与会话/产物/诊断共用 <appRoot>/sessions.db（d.indexDB）：迁移 006 只跑
//     一次，搜索页可能在用户还没跑过任何 run 时就被打开；
//   - 跨边界错误一律结构化（CallError），前端按 Code 分支显示本地化文案。
//
// 索引是派生数据：Search 不自动重建（"索引未建立"必须是可观察的真实状态），
// 重建由 RebuildSearchIndex 显式触发。这样"重建"是一个用户可见、可重复的动作，
// 而不是藏在读取路径里的隐式行为。

import (
	"context"
	"strings"
	"time"

	"my-agent-go/internal/resources"
	"my-agent-go/internal/search"
)

// SearchResult 是一次全局搜索的结果。
//
// Availability 为 unavailable 时 Items 一定为空，Error 通常为空：
// "索引还没建"不是失败，是一个需要用户点"重建索引"的状态。
type SearchResult struct {
	Query        string        `json:"query"`
	Availability string        `json:"availability"`
	IndexBuiltAt *time.Time    `json:"indexBuiltAt,omitempty"`
	Items        []search.Item `json:"items"`
	Error        *CallError    `json:"error,omitempty"`
}

// SearchIndexStatusResult 是索引状态与最近一次重建的结果。
type SearchIndexStatusResult struct {
	Availability string         `json:"availability"`
	BuiltAt      *time.Time     `json:"builtAt,omitempty"`
	Summary      search.Summary `json:"summary"`
	Error        *CallError     `json:"error,omitempty"`
}

// searchService 返回应用级搜索服务。
//
// 每次调用现装配：服务本身无状态（状态在库里），来源适配器只读配置存储。
func (d *Desktop) searchService() (*search.Service, error) {
	db, err := d.indexDB()
	if err != nil {
		return nil, err
	}
	return search.New(db, desktopSearchSource{d: d})
}

// Search 执行一次全局搜索。kinds 为空表示全部类别；limit<=0 用服务默认值。
func (d *Desktop) Search(query string, kinds []string, limit int) SearchResult {
	out := SearchResult{Query: strings.TrimSpace(query), Availability: search.AvailabilityUnavailable, Items: []search.Item{}}
	svc, err := d.searchService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	res, err := svc.Search(context.Background(), search.Query{
		Text:  query,
		Kinds: kinds,
		// 搜索范围是当前工作区：别的项目的会话/产物不该出现在这里。
		WorkspaceID: strings.TrimSpace(d.currentWorkspaceIDBestEffort()),
		Limit:       limit,
	})
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Query = res.Query
	out.Availability = res.Availability
	out.IndexBuiltAt = res.IndexBuiltAt
	if res.Items != nil {
		out.Items = res.Items
	}
	return out
}

// RebuildSearchIndex 从权威表重建索引（幂等；重启后可直接重跑）。
func (d *Desktop) RebuildSearchIndex() SearchIndexStatusResult {
	return d.searchIndexResult(func(svc *search.Service) (search.Status, error) {
		if _, err := svc.Rebuild(context.Background()); err != nil {
			return search.Status{}, err
		}
		return svc.Status(context.Background())
	})
}

// SearchIndexStatus 读索引状态，不触发重建。
func (d *Desktop) SearchIndexStatus() SearchIndexStatusResult {
	return d.searchIndexResult(func(svc *search.Service) (search.Status, error) {
		return svc.Status(context.Background())
	})
}

func (d *Desktop) searchIndexResult(fn func(*search.Service) (search.Status, error)) SearchIndexStatusResult {
	out := SearchIndexStatusResult{Availability: search.AvailabilityUnavailable}
	svc, err := d.searchService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	st, err := fn(svc)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Availability = st.Availability
	out.BuiltAt = st.BuiltAt
	out.Summary = st.Summary
	return out
}

// currentWorkspaceIDBestEffort 尽量解析当前工作区 id。
//
// 解析不出来（还没配工作区）时返回空：空 = 不做工作区过滤，而不是"搜索不可用"。
// 会话列表本身也会因为没工作区而空着，两处口径保持一致。
func (d *Desktop) currentWorkspaceIDBestEffort() string {
	id, err := d.currentWorkspaceID(context.Background())
	if err != nil {
		return ""
	}
	return string(id)
}

// desktopSearchSource 是配置存储里的可检索来源（Agent/Profile 与 Skill）。
//
// 它们不在 SQLite 里，所以由装配层读取；internal/search 只认"给我一组 Doc"。
type desktopSearchSource struct {
	d *Desktop
}

// Agents 列出配置档（V1 的 Agent）。
func (s desktopSearchSource) Agents(ctx context.Context) ([]search.Doc, error) {
	_, profSvc, err := s.d.coreServices()
	if err != nil {
		return nil, err
	}
	items, err := profSvc.List(ctx)
	if err != nil {
		return nil, err
	}
	docs := make([]search.Doc, 0, len(items))
	for _, p := range items {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		docs = append(docs, search.Doc{
			Kind:  search.KindAgent,
			RefID: string(p.ID),
			Route: search.RouteFor(search.KindAgent, string(p.ID)),
			Title: name,
			// 模型引用是可检索的次要信息：用户常记得"那个用 sonnet 的 profile"。
			Subtitle: strings.TrimSpace(p.ModelRef),
		})
	}
	return docs, nil
}

// Skills 列出托管技能。
func (s desktopSearchSource) Skills(ctx context.Context) ([]search.Doc, error) {
	svc, err := s.d.resourceServices()
	if err != nil {
		return nil, err
	}
	set, err := svc.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	docs := make([]search.Doc, 0, len(set.Items))
	for _, item := range set.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		// 只有 workspace scope 的技能才限定工作区。user scope 的技能对所有工作区生效，
		// 而它的 WorkspaceID 字段仍是 "ws-default"（管理目录用）；照抄字段会让
		// "我在 A 项目里搜不到自己的全局技能"。
		workspaceID := ""
		if item.Scope == resources.ScopeWorkspace {
			workspaceID = strings.TrimSpace(item.WorkspaceID)
		}
		docs = append(docs, search.Doc{
			Kind:        search.KindSkill,
			RefID:       item.ID,
			WorkspaceID: workspaceID,
			Route:       search.RouteFor(search.KindSkill, item.ID),
			Title:       name,
			Subtitle:    strings.TrimSpace(item.Description),
		})
	}
	return docs, nil
}
