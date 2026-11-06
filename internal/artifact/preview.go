package artifact

// C07 产物预览：只服务白名单内的媒体类型，且必须在核对之后。
//
// 为什么预览也要核对：预览是"把这个文件的内容显示给用户"，如果不核对，
// 一个被替换过的文件就会以"产物预览"的名义把别的内容端到界面上。
// 预览与导出共用同一条 resolveSource + observeFile 路径。
//
// 白名单只认明确的类型：可执行文件（application/x-msdownload 等）、未知类型、
// 以及内容不是有效 UTF-8 的假文本，一律结构化拒绝（UNSUPPORTED）。
// SVG 不在白名单里：它是可执行脚本的 XML，预览一个可执行文档不是本版本的承诺。

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// 预览种类。
const (
	PreviewKindText     = "text"
	PreviewKindImage    = "image"
	PreviewKindDocument = "document"
)

const (
	// maxPreviewTextBytes 是文本预览内联的字节上限（超出标记 truncated）。
	maxPreviewTextBytes = 64 << 10
	// maxPreviewImageBytes 是图片预览内联的原始字节上限（base64 后约 5.3 MiB）。
	maxPreviewImageBytes = 4 << 20
)

// previewWhitelist 是允许预览的媒体类型 → 预览种类。
//
// 只收"渲染成文本/图片不需要执行任何东西"的类型。application/pdf 归为 document：
// 它不在内联字节里，只给"已登记、可导出"的结论。
var previewWhitelist = map[string]string{
	"text/plain":                PreviewKindText,
	"text/markdown":             PreviewKindText,
	"text/x-markdown":           PreviewKindText,
	"text/csv":                  PreviewKindText,
	"text/tab-separated-values": PreviewKindText,
	"application/json":          PreviewKindText,
	"application/x-ndjson":      PreviewKindText,
	"image/png":                 PreviewKindImage,
	"image/jpeg":                PreviewKindImage,
	"image/gif":                 PreviewKindImage,
	"image/webp":                PreviewKindImage,
	"application/pdf":           PreviewKindDocument,
}

// Preview 是一次产物预览。
//
// Verification 是这次预览所依据的核对结论：只有 verified 的产物才会走到内容字段。
type Preview struct {
	ArtifactID   string       `json:"artifactId"`
	DisplayName  string       `json:"displayName"`
	MediaType    string       `json:"mediaType"`
	Kind         string       `json:"kind"`
	Size         int64        `json:"size"`
	SHA256       string       `json:"sha256"`
	Text         string       `json:"text,omitempty"`
	DataBase64   string       `json:"dataBase64,omitempty"`
	Truncated    bool         `json:"truncated"`
	Note         string       `json:"note,omitempty"`
	Verification Verification `json:"verification"`
}

// PreviewArtifact 预览一条产物。
//
// 顺序：白名单（读字节之前）→ 重新证明路径 → 读取并核对 → 内联内容。
// 任何一步失败都不会返回"看起来像内容"的半份结果。
func (s *Service) PreviewArtifact(ctx context.Context, id string) (Preview, error) {
	if s == nil {
		return Preview{}, contracts.NewAppError(contracts.CodeInternal, "产物服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	item, err := s.load(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	mediaType := normalizeMediaType(item.MediaType)
	kind, ok := previewWhitelist[mediaType]
	if !ok {
		return Preview{}, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"不支持的预览媒体类型 %q；允许的类型：%s", item.MediaType, strings.Join(allowedPreviewTypes(), "、")), false)
	}

	src, problem := s.resolveSource(ctx, item)
	if problem != nil {
		return Preview{}, verificationError(item, problem)
	}
	keep := maxPreviewTextBytes
	if kind == PreviewKindImage {
		keep = maxPreviewImageBytes
	}
	obs, err := observeFile(src, keep)
	if err != nil {
		return Preview{}, contracts.NewAppError(contracts.CodeVerificationFailed,
			"读取产物内容失败: "+err.Error(), true)
	}
	if !obs.regular {
		return Preview{}, contracts.NewAppError(contracts.CodeVerificationFailed, "产物不再是普通文件", false)
	}
	size := obs.size
	ver := Verification{
		Status:         StatusVerified,
		ObservedSHA256: obs.sha256,
		ObservedSize:   &size,
		CheckedAt:      s.now().UTC(),
	}
	if obs.size != item.Size || obs.sha256 != item.SHA256 {
		ver.Status = StatusDrifted
		ver.Reason = fmt.Sprintf("预览时读到的内容与登记不符（登记 %d 字节 %s，实际 %d 字节 %s）",
			item.Size, shortSHA(item.SHA256), obs.size, shortSHA(obs.sha256))
		return Preview{}, verificationError(item, &ver)
	}

	out := Preview{
		ArtifactID:   item.ID,
		DisplayName:  item.DisplayName,
		MediaType:    mediaType,
		Kind:         kind,
		Size:         item.Size,
		SHA256:       item.SHA256,
		Verification: ver,
	}
	switch kind {
	case PreviewKindText:
		text, ok := textPrefix(obs.head)
		if !ok {
			return Preview{}, contracts.NewAppError(contracts.CodeUnsupported,
				"产物内容不是有效 UTF-8 文本，不能作为文本预览: "+item.DisplayName, false)
		}
		out.Text = text
		out.Truncated = obs.size > int64(len(obs.head))
	case PreviewKindImage:
		out.DataBase64 = base64.StdEncoding.EncodeToString(obs.head)
		out.Truncated = obs.size > int64(len(obs.head))
	case PreviewKindDocument:
		out.Note = "该类型不内联预览：请使用导出（ExportArtifact）后在查看器里打开"
	}
	return out, nil
}

// allowedPreviewTypes 返回排序后的白名单，用于错误文案（前端不必自己维护清单）。
func allowedPreviewTypes() []string {
	out := make([]string, 0, len(previewWhitelist))
	for mt := range previewWhitelist {
		out = append(out, mt)
	}
	sort.Strings(out)
	return out
}

// textPrefix 把前缀字节转成可显示的文本。
//
// 截断可能切断一个多字节字符的尾部：这里最多退 3 个字节直到整段是有效 UTF-8。
// 内容本身（未截断时）不是有效 UTF-8 就返回 false——那种文件不该当文本预览。
func textPrefix(head []byte) (string, bool) {
	if !utf8.Valid(head) {
		// 只在"末尾被截断"时退字节；中间有坏字节说明它根本不是文本。
		for trim := 1; trim <= 3 && trim < len(head); trim++ {
			if utf8.Valid(head[:len(head)-trim]) {
				return string(head[:len(head)-trim]), true
			}
		}
		return "", false
	}
	if strings.IndexByte(string(head), 0) >= 0 {
		return "", false
	}
	return string(head), true
}

// nowUTC 是预览/核对共用的时间点（测试可注入）。
func (s *Service) nowUTC() time.Time { return s.now().UTC() }
