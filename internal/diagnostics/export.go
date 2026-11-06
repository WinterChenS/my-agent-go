package diagnostics

// 诊断导出：把可观测事实写成一个本机 JSON 文件。不上传任何地方。
//
// 包里有什么（以及为什么这些字段在脱敏后仍然保留）：
//   - app.version / app.goVersion / app.platform：**版本与平台**是复现问题的前提，
//     脱敏规则不碰它们（统一格式的版本号里没有秘密形态）；
//   - database.schemaVersion：判断"这份包是不是旧 schema 下产生的"；
//   - runs[]：runId / sessionId / state / piVersion / provider / model /
//     configRevision / lastSeq / 时间 / usage / budget / error.code。
//     错误码与时间戳原样保留——它们是定位键；free-text 的 error.message
//     会被脱敏（里面最常带密钥与路径）；
//   - events[]：eventId / seq / type / occurredAt / payload。eventId 是
//     "<runId>-<seq>" 形式的定位键，保留；payload 在**写入时与导出时**各脱敏一次；
//   - artifacts[]：产物索引（id / 展示名 / 媒体类型 / 大小 / sha256）。展示名与
//     路径可能含用户名/秘密，会被脱敏；id 与 hash 保留。
//
// 写入顺序：解析目标 → 组装 → 脱敏 → 同目录临时文件 → fsync → rename。
// 目标只有"旧的完整版本"和"新的完整版本"两种状态，失败不留半份。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/redact"
)

const (
	// bundleSchemaVersion 是诊断包文档的格式版本。
	bundleSchemaVersion = 1
	// bundleKind 是包标识：读取侧靠它认出"这是 Anvil 诊断包"。
	bundleKind = "anvil.diagnostics"
	// maxBundleBytes 是诊断包的大小上限。超限是拒绝，不是截断：
	// 半份诊断包会让人误以为"就没有这些记录"。
	maxBundleBytes = 16 << 20
	// maxBundleEvents / maxBundleArtifacts 是包内事件/产物条数上限。
	maxBundleEvents    = 2000
	maxBundleArtifacts = 500
	// bundleTempPrefix 是同目录临时文件前缀，便于在诊断目录里认出残留。
	bundleTempPrefix = ".diagnostics-"
)

// ExportRequest 是诊断导出请求。
//
// RunIDs/SessionID/WorkspaceID/From/To 是范围（零值表示全部）；
// Target 为空时自动生成 bundle 文件名，且只会落在诊断根内。
type ExportRequest struct {
	RunIDs      []string  `json:"runIds"`
	SessionID   string    `json:"sessionId"`
	WorkspaceID string    `json:"workspaceId"`
	From        time.Time `json:"from,omitempty"`
	To          time.Time `json:"to,omitempty"`
	Target      string    `json:"target"`
}

// ExportResult 是一次诊断导出的结果。
type ExportResult struct {
	Path          string    `json:"path"`
	Bytes         int       `json:"bytes"`
	SHA256        string    `json:"sha256"`
	Redactions    int       `json:"redactions"`
	Runs          int       `json:"runs"`
	Events        int       `json:"events"`
	Artifacts     int       `json:"artifacts"`
	SchemaVersion int       `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
}

// appFacts 是包里的环境身份。
type appFacts struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Platform  string `json:"platform"`
}

// dbFacts 是索引库身份（schema 版本决定"能不能读懂这些表"）。
type dbFacts struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path,omitempty"`
}

// redactionFacts 说明脱敏做了什么。
//
// 刻意**不写规则 pattern**：pattern 本身就是含 `authorization:`、`/home/` 这类
// 片段的文本，会被自己的规则改写（诊断包里的"规则清单"变成一个被脱敏过的字符串），
// 严重时还会让整份文档不再是合法 JSON。这里给的是规则条数与摘要：
// 条数能核对"套了几条"，摘要能在不同构建之间比对规则表有没有悄悄变。
type redactionFacts struct {
	Hits            int    `json:"hits"`
	BaseRules       int    `json:"baseRules"`
	DiagnosticRules int    `json:"diagnosticRules"`
	RulesDigest     string `json:"rulesDigest"`
}

// eventBundle 是包里的一条事件。
type eventBundle struct {
	RunID            string          `json:"runId"`
	Seq              uint64          `json:"seq"`
	EventID          string          `json:"eventId"`
	SessionID        string          `json:"sessionId,omitempty"`
	Type             string          `json:"type"`
	OccurredAt       string          `json:"occurredAt"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	PayloadTruncated bool            `json:"payloadTruncated,omitempty"`
}

// artifactBundle 是包里的一条产物索引。
type artifactBundle struct {
	ArtifactID    string `json:"artifactId"`
	WorkspaceID   string `json:"workspaceId,omitempty"`
	RunID         string `json:"runId,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	CanonicalPath string `json:"canonicalPath,omitempty"`
	MediaType     string `json:"mediaType,omitempty"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
}

// bundle 是诊断包文档。
type bundle struct {
	SchemaVersion int              `json:"schemaVersion"`
	Kind          string           `json:"kind"`
	GeneratedAt   string           `json:"generatedAt"`
	App           appFacts         `json:"app"`
	Database      dbFacts          `json:"database"`
	Range         UsageRange       `json:"range"`
	Usage         UsageSummary     `json:"usage"`
	Runs          []RunRecord      `json:"runs"`
	Events        []eventBundle    `json:"events"`
	Artifacts     []artifactBundle `json:"artifacts"`
	Redaction     redactionFacts   `json:"redaction"`
}

// ExportDiagnostics 组装并写出一份诊断包。
func (s *Service) ExportDiagnostics(ctx context.Context, req ExportRequest) (ExportResult, error) {
	if s == nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "诊断服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	targetPath, err := s.resolveTarget(req.Target)
	if err != nil {
		return ExportResult{}, err
	}
	usageRange := UsageRange{
		From:        nilIfZero(req.From),
		To:          nilIfZero(req.To),
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		SessionID:   strings.TrimSpace(req.SessionID),
		RunIDs:      cleanIDs(req.RunIDs),
	}
	rows, err := s.readRunUsage(ctx, usageRange)
	if err != nil {
		return ExportResult{}, err
	}
	runIDs := make([]string, 0, len(rows))
	runs := make([]RunRecord, 0, len(rows))
	for _, row := range rows {
		runIDs = append(runIDs, row.RunID)
		runs = append(runs, recordFromRow(row))
	}

	events, err := s.readEventsForRuns(ctx, runIDs, maxBundleEvents)
	if err != nil {
		return ExportResult{}, err
	}
	artifacts, err := s.readArtifactsForRuns(ctx, runIDs, maxBundleArtifacts)
	if err != nil {
		return ExportResult{}, err
	}

	schemaVersion, err := s.db.SchemaVersion(ctx)
	if err != nil {
		return ExportResult{}, err
	}
	doc := bundle{
		SchemaVersion: bundleSchemaVersion,
		Kind:          bundleKind,
		GeneratedAt:   formatTS(s.now()),
		App: appFacts{
			Version:   s.version,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		},
		Database:  dbFacts{SchemaVersion: schemaVersion, Path: s.db.Path()},
		Range:     usageRange,
		Usage:     summarizeUsage(usageRange, rows),
		Runs:      runs,
		Events:    events,
		Artifacts: artifacts,
		Redaction: redactionFactSummary(),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "序列化诊断包失败: "+err.Error(), false)
	}
	if len(raw) > maxBundleBytes {
		return ExportResult{}, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"诊断包超过大小上限（> %d 字节）：请缩小范围（runIds/时间区间）后重试", maxBundleBytes), false)
	}
	out, hits, err := redactDocument(raw)
	if err != nil {
		return ExportResult{}, err
	}
	doc.Redaction.Hits = hits
	// 命中数要在脱敏之后写回；重编码一次（仍然走同一套脱敏，幂等）。
	withHits, marshalErr := json.Marshal(doc)
	if marshalErr == nil && json.Valid(withHits) {
		out, hits, err = redactDocument(withHits)
		if err != nil {
			return ExportResult{}, err
		}
	}
	if len(out) > maxBundleBytes {
		return ExportResult{}, contracts.NewAppError(contracts.CodeUnsupported,
			fmt.Sprintf("诊断包超过大小上限（> %d 字节）", maxBundleBytes), false)
	}
	if err := writeFileAtomic(targetPath, out); err != nil {
		return ExportResult{}, err
	}
	sum := sha256.Sum256(out)
	return ExportResult{
		Path:          targetPath,
		Bytes:         len(out),
		SHA256:        hex.EncodeToString(sum[:]),
		Redactions:    hits,
		Runs:          len(runs),
		Events:        len(events),
		Artifacts:     len(artifacts),
		SchemaVersion: bundleSchemaVersion,
		GeneratedAt:   s.now().UTC(),
	}, nil
}

// readEventsForRuns 批量读事件。runIDs 为空时不查（避免 "IN ()" 语法错误）。
func (s *Service) readEventsForRuns(ctx context.Context, runIDs []string, limit int) ([]eventBundle, error) {
	out := []eventBundle{}
	ids := cleanIDs(runIDs)
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT run_id, seq, event_id, session_id, type, occurred_at, payload, payload_truncated
		  FROM run_events WHERE run_id IN (`+placeholders(len(ids))+`)
		 ORDER BY run_id, seq LIMIT ?`, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取诊断事件失败: "+err.Error(), true)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ev        eventBundle
			seq       int64
			truncated int
			payload   string
		)
		if err := rows.Scan(&ev.RunID, &seq, &ev.EventID, &ev.SessionID, &ev.Type,
			&ev.OccurredAt, &payload, &truncated); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描诊断事件失败: "+err.Error(), true)
		}
		if seq > 0 {
			ev.Seq = uint64(seq)
		}
		ev.PayloadTruncated = truncated != 0
		if strings.TrimSpace(payload) != "" {
			// 再脱敏一次（写入时已经脱敏过，这里是纵深防御）：文档级的文本脱敏
			// 万一因为其它字段破坏了 JSON 有效性而退化成"逐值脱敏"，
			// 嵌套载荷里的"键邻接"秘密就漏了——在载荷这一层先处理掉。
			safe, truncatedAgain, _ := redactPayload([]byte(payload))
			ev.PayloadTruncated = ev.PayloadTruncated || truncatedAgain
			payload = safe
			if json.Valid([]byte(payload)) {
				ev.Payload = json.RawMessage(payload)
			} else {
				// 不是合法 JSON 的载荷（外部写入/旧数据）按字符串保留：
				// 包本身必须始终是合法 JSON，否则整份诊断包读不了。
				enc, _ := json.Marshal(payload)
				ev.Payload = enc
			}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "诊断事件结果异常: "+err.Error(), true)
	}
	return out, nil
}

// readArtifactsForRuns 批量读产物索引。
func (s *Service) readArtifactsForRuns(ctx context.Context, runIDs []string, limit int) ([]artifactBundle, error) {
	out := []artifactBundle{}
	ids := cleanIDs(runIDs)
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, workspace_id, run_id, display_name, canonical_path, media_type, size_bytes, sha256, created_at
		  FROM artifacts WHERE run_id IN (`+placeholders(len(ids))+`)
		 ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取诊断产物失败: "+err.Error(), true)
	}
	defer rows.Close()
	for rows.Next() {
		var item artifactBundle
		if err := rows.Scan(&item.ArtifactID, &item.WorkspaceID, &item.RunID, &item.DisplayName,
			&item.CanonicalPath, &item.MediaType, &item.Size, &item.SHA256, &item.CreatedAt); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描诊断产物失败: "+err.Error(), true)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "诊断产物结果异常: "+err.Error(), true)
	}
	return out, nil
}

// redactionFactSummary 汇总"套了哪些规则"的可核对信息（条数 + 摘要，不含 pattern）。
func redactionFactSummary() redactionFacts {
	base := redact.Rules()
	diag := redact.DiagnosticRules()
	h := sha256.New()
	for _, r := range base {
		h.Write([]byte("base:" + r.Pattern + "=>" + r.Replacement + "\n"))
	}
	for _, r := range diag {
		h.Write([]byte("diagnostics:" + r.Pattern + "=>" + r.Replacement + "\n"))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return redactionFacts{
		BaseRules:       len(base),
		DiagnosticRules: len(diag),
		RulesDigest:     sum[:16],
	}
}

// nilIfZero 把零值时间当作"没有边界"。
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// resolveTarget 把 target 解析成"诊断根内的普通文件路径"。
func (s *Service) resolveTarget(target string) (string, error) {
	raw := strings.TrimSpace(target)
	if raw == "" {
		raw = "diagnostics-" + s.now().UTC().Format("20060102T150405Z") + ".json"
	}
	if strings.ContainsRune(raw, 0) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "诊断包目标路径包含 NUL 字节", false)
	}
	if err := lexicalRejectPath(raw); err != nil {
		return "", err
	}
	if hasDotDotSegment(raw) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"诊断包目标不允许用 .. 拼接: "+raw, false)
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.root, raw)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(s.root, abs)
	if err != nil || escapesRoot(rel) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "诊断包目标越界: "+abs, false)
	}
	if link, err := linkBelowRoot(s.guard.Roots()[0], rel); err != nil {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"诊断包目标无法解析: "+abs+" : "+err.Error(), false)
	} else if link != "" {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"诊断包目标经过符号链接/目录联接，拒绝写入: "+link, false)
	}
	dec, err := s.guard.Check(abs)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(dec.Real)
	fi, err := os.Stat(dir)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported, "诊断包目标的父路径不是目录: "+dir, false)
		}
	case errors.Is(err, fs.ErrNotExist):
		return "", contracts.NewAppError(contracts.CodeNotFound, "诊断包目标目录不存在: "+dir, false)
	default:
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"诊断包目标目录不可访问: "+dir+"（"+err.Error()+"）", false)
	}
	if fi, err := os.Lstat(dec.Real); err == nil {
		if fi.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported, "诊断包目标是目录: "+dec.Real, false)
		}
		if !fi.Mode().IsRegular() {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"诊断包目标不是普通文件: "+dec.Real, false)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"诊断包目标不可访问: "+dec.Real+"（"+err.Error()+"）", false)
	}
	return dec.Real, nil
}

// writeFileAtomic 写入 target：同目录临时文件 → fsync → rename。
func writeFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, bundleTempPrefix+"*.tmp")
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建诊断包临时文件失败: "+err.Error(), false)
	}
	tmpPath := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		discard()
		return contracts.NewAppError(contracts.CodeInternal, "写入诊断包临时文件失败: "+err.Error(), false)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return contracts.NewAppError(contracts.CodeInternal, "刷新诊断包临时文件失败: "+err.Error(), false)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return contracts.NewAppError(contracts.CodeInternal, "关闭诊断包临时文件失败: "+err.Error(), false)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return contracts.NewAppError(contracts.CodeInternal, "发布诊断包失败: "+err.Error(), false)
	}
	return nil
}
