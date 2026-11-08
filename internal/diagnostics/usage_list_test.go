package diagnostics

// B2：跨 run 的 usage 列表（观测页的"最近 run / 最近错误"）。
//
// 这里钉住三件事：
//  1. 分页是**确定性**的：排序键是 (updated_at DESC, run_id DESC)，
//     与记录插入的物理顺序无关；两页之间不重叠、不遗漏；
//  2. 有更新的记录插进来时，已经取到的第一页不变（不能因为一次插入导致整页漂移）；
//  3. 空列表是"查过了，没有"——不是错误；查询本身失败才是错误（由调用方区分）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// seedSameInstant 用同一个 updated_at 落 5 行：排序只能由 run_id 决定。
func seedSameInstant(t *testing.T, env testEnv, ids []string, ts string) {
	t.Helper()
	for _, id := range ids {
		seedRunUsage(t, env.db, seedRow{
			RunID:        id,
			SessionID:    "sess-" + id,
			Availability: contracts.UsageReported,
			Input:        ptrInt(1),
			Output:       ptrInt(1),
			Total:        ptrInt(2),
			UpdatedAt:    ts,
		})
	}
}

func diagAppErrCode(err error) string {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr != nil {
		return appErr.Code
	}
	return ""
}

func runIDsOf(items []RunRecord) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.RunID)
	}
	return out
}

func TestListRunUsagePaginatesDeterministically(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	const ts = "2026-11-05T10:00:00.000000000Z"
	// 故意打乱插入顺序：物理顺序不是排序依据。
	seedSameInstant(t, env, []string{"run-c", "run-a", "run-e", "run-b", "run-d"}, ts)

	page1, err := env.svc.ListRunUsage(ctx, RunUsageQuery{Limit: 2})
	if err != nil {
		t.Fatalf("第一页: %v", err)
	}
	if got := strings.Join(runIDsOf(page1.Items), ","); got != "run-e,run-d" {
		t.Fatalf("第一页顺序 = %s，期望 run-e,run-d（updated_at 相同则按 run_id 降序）", got)
	}
	if page1.Total != 5 {
		t.Fatalf("Total = %d，期望 5（总数不受分页影响）", page1.Total)
	}
	if page1.Limit != 2 || page1.Offset != 0 {
		t.Fatalf("分页回显 = limit=%d offset=%d，期望 2/0", page1.Limit, page1.Offset)
	}

	page2, err := env.svc.ListRunUsage(ctx, RunUsageQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("第二页: %v", err)
	}
	if got := strings.Join(runIDsOf(page2.Items), ","); got != "run-c,run-b" {
		t.Fatalf("第二页顺序 = %s，期望 run-c,run-b", got)
	}
	page3, err := env.svc.ListRunUsage(ctx, RunUsageQuery{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("第三页: %v", err)
	}
	if got := strings.Join(runIDsOf(page3.Items), ","); got != "run-a" {
		t.Fatalf("第三页顺序 = %s，期望 run-a", got)
	}

	// 三页必须恰好覆盖全部 5 行，不重叠、不遗漏。
	seen := map[string]int{}
	for _, page := range [][]RunRecord{page1.Items, page2.Items, page3.Items} {
		for _, it := range page {
			seen[it.RunID]++
		}
	}
	if len(seen) != 5 {
		t.Fatalf("分页覆盖 %d 个 run，期望 5", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("run %s 在分页里出现 %d 次（分页窗口重叠）", id, n)
		}
	}

	// 插入一条**更旧**的记录：第一页必须不变（旧记录排在最后，不挤走已取到的行）。
	seedRunUsage(t, env.db, seedRow{
		RunID:     "run-0",
		SessionID: "sess-run-0",
		UpdatedAt: "2026-11-05T09:00:00.000000000Z",
	})
	again, err := env.svc.ListRunUsage(ctx, RunUsageQuery{Limit: 2})
	if err != nil {
		t.Fatalf("插入旧记录后第一页: %v", err)
	}
	if got := strings.Join(runIDsOf(again.Items), ","); got != "run-e,run-d" {
		t.Fatalf("插入更旧记录后第一页 = %s，期望仍是 run-e,run-d", got)
	}
	if again.Total != 6 {
		t.Fatalf("插入后 Total = %d，期望 6", again.Total)
	}
}

func TestListRunUsageFiltersAndLimitBounds(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedRunUsage(t, env.db, seedRow{
		RunID: "run-ws-1", SessionID: "sess-a", Availability: contracts.UsageReported,
		Input: ptrInt(1), Output: ptrInt(1), Total: ptrInt(2),
		UpdatedAt: "2026-11-05T10:00:00.000000000Z",
	})
	seedRunUsage(t, env.db, seedRow{
		RunID: "run-ws-2", SessionID: "sess-b", Availability: contracts.UsageReported,
		Input: ptrInt(1), Output: ptrInt(1), Total: ptrInt(2),
		UpdatedAt: "2026-11-05T11:00:00.000000000Z",
	})

	bySession, err := env.svc.ListRunUsage(ctx, RunUsageQuery{
		UsageRange: UsageRange{SessionID: "sess-a"},
	})
	if err != nil {
		t.Fatalf("按会话过滤: %v", err)
	}
	if len(bySession.Items) != 1 || bySession.Items[0].RunID != "run-ws-1" {
		t.Fatalf("按会话过滤结果 = %v", runIDsOf(bySession.Items))
	}
	if bySession.Total != 1 {
		t.Fatalf("按会话过滤 Total = %d，期望 1（count 与 where 必须同一份条件）", bySession.Total)
	}

	// limit=0 表示"用默认值"，结果必须回显真实生效的 limit。
	def, err := env.svc.ListRunUsage(ctx, RunUsageQuery{})
	if err != nil {
		t.Fatalf("默认分页: %v", err)
	}
	if def.Limit != DefaultRunUsageLimit {
		t.Fatalf("默认 limit = %d，期望 %d", def.Limit, DefaultRunUsageLimit)
	}

	// 超上限被夹到 MaxRunUsageLimit，并且回显的是**实际**上限（不能悄悄少给）。
	big, err := env.svc.ListRunUsage(ctx, RunUsageQuery{Limit: MaxRunUsageLimit + 1000})
	if err != nil {
		t.Fatalf("超上限分页: %v", err)
	}
	if big.Limit != MaxRunUsageLimit {
		t.Fatalf("超上限 limit 回显 = %d，期望夹到 %d", big.Limit, MaxRunUsageLimit)
	}

	// 负值不是"随便当 0"：入参不合法必须拒绝，否则两种写法会得到不同页。
	for _, q := range []RunUsageQuery{{Offset: -1}, {Limit: -5}} {
		if _, err := env.svc.ListRunUsage(ctx, q); err == nil {
			t.Fatalf("非法分页 %+v 必须被拒绝", q)
		} else if code := diagAppErrCode(err); code != contracts.CodeInvalidInput {
			t.Fatalf("非法分页错误码 = %q，期望 %q（err=%v）", code, contracts.CodeInvalidInput, err)
		}
	}
}

func TestListRunUsageEmptyIsNotAnError(t *testing.T) {
	env := newTestEnv(t)
	res, err := env.svc.ListRunUsage(context.Background(), RunUsageQuery{})
	if err != nil {
		t.Fatalf("空库列表必须是成功结果: %v", err)
	}
	if res.Items == nil {
		t.Fatal("空结果必须是空数组而不是 nil（nil 会被序列化成 null，界面无法当列表用）")
	}
	if len(res.Items) != 0 || res.Total != 0 {
		t.Fatalf("空库结果 = %d 项 / total %d", len(res.Items), res.Total)
	}
}
