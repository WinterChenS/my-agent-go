package pi

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// chunkReader 按随机长度切片返回数据，用于模拟 pipe 的任意分块边界
// （包括把一个多字节 UTF-8 字符切成两半）。
type chunkReader struct {
	data     []byte
	pos      int
	rnd      *rand.Rand
	maxChunk int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := 1 + r.rnd.Intn(r.maxChunk)
	if remain := len(r.data) - r.pos; n > remain {
		n = remain
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// collectFrames 读完所有帧，返回原始字节串与错误。
func collectFrames(t *testing.T, f *Framer) ([]string, error) {
	t.Helper()
	var got []string
	for {
		b, err := f.Next()
		if errors.Is(err, io.EOF) {
			return got, nil
		}
		if err != nil {
			return got, err
		}
		got = append(got, string(b))
	}
}

// TestLFFramingUnicodeAndLargeRecord 对应 RPC01：
// LF 随机分块 / CRLF / U+2028 / UTF-8 多字节 / 128KiB 记录均完整还原，
// 16 MiB 恰好允许、超 1 字节报 ProtocolRecordTooLarge，且不截断 JSON。
func TestLFFramingUnicodeAndLargeRecord(t *testing.T) {
	t.Run("随机分块还原多字节与U+2028", func(t *testing.T) {
		// U+2028 必须以真实字符（E2 80 A8）出现；写成 \u2028 的 6 个 ASCII 字节
		// 不构成分帧风险，测不出问题。
		want := []string{
			`{"type":"message_update","delta":"第一段` + "\u2028" + `第二段"}`,
			`{"type":"message_update","delta":"emoji🙂与增补平面𝄞"}`,
			`{"type":"tool","name":"读取文件","args":{"path":"D:\\项目\\a.txt"}}`,
			`{"type":"message_update","delta":"` + "\u2029" + `段落分隔"}`,
		}
		var buf bytes.Buffer
		for _, r := range want {
			buf.WriteString(r)
			buf.WriteString("\n")
		}

		src := &chunkReader{data: buf.Bytes(), rnd: rand.New(rand.NewSource(42)), maxChunk: 3}
		got, err := collectFrames(t, NewFramer(src, 0))
		if err != nil {
			t.Fatalf("分帧失败: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("记录未被完整还原\n期望 %d 条: %#v\n实际 %d 条: %#v", len(want), want, len(got), got)
		}
	})

	t.Run("CRLF 剥离尾部CR", func(t *testing.T) {
		got, err := collectFrames(t, NewFramer(strings.NewReader("{\"a\":1}\r\n{\"b\":2}\n"), 0))
		if err != nil {
			t.Fatalf("分帧失败: %v", err)
		}
		want := []string{`{"a":1}`, `{"b":2}`}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("CRLF 处理错误：期望 %#v，实际 %#v", want, got)
		}
	})

	t.Run("128KiB记录完整", func(t *testing.T) {
		pad := strings.Repeat("值", 128<<10/3+1)
		rec := `{"type":"message_update","delta":"` + pad + `"}`
		if len(rec) < 128<<10 {
			t.Fatalf("测试数据构造错误：只有 %d 字节", len(rec))
		}
		src := &chunkReader{data: []byte(rec + "\n"), rnd: rand.New(rand.NewSource(7)), maxChunk: 977}
		got, err := collectFrames(t, NewFramer(src, 0))
		if err != nil {
			t.Fatalf("分帧失败: %v", err)
		}
		if len(got) != 1 || got[0] != rec {
			t.Fatalf("128KiB 记录未完整还原（得到 %d 条，首条 %d 字节）", len(got), len(got[0]))
		}
	})

	t.Run("恰好16MiB允许", func(t *testing.T) {
		prefix, suffix := `{"pad":"`, `"}`
		pad := strings.Repeat("a", MaxRecordBytes-len(prefix)-len(suffix))
		rec := prefix + pad + suffix
		if len(rec) != MaxRecordBytes {
			t.Fatalf("测试数据长度应为 %d，实际 %d", MaxRecordBytes, len(rec))
		}
		f := NewFramer(strings.NewReader(rec+"\n"), 0)
		got, err := f.Next()
		if err != nil {
			t.Fatalf("恰好 16 MiB 应被允许，实际报错: %v", err)
		}
		if len(got) != MaxRecordBytes {
			t.Fatalf("记录长度应为 %d，实际 %d", MaxRecordBytes, len(got))
		}
		if _, err := f.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("读完后应返回 io.EOF，实际 %v", err)
		}
	})

	t.Run("超过16MiB一字节报ProtocolRecordTooLarge", func(t *testing.T) {
		prefix, suffix := `{"pad":"`, `"}`
		pad := strings.Repeat("a", MaxRecordBytes-len(prefix)-len(suffix)+1)
		rec := prefix + pad + suffix
		if len(rec) != MaxRecordBytes+1 {
			t.Fatalf("测试数据长度应为 %d，实际 %d", MaxRecordBytes+1, len(rec))
		}
		_, err := NewFramer(strings.NewReader(rec+"\n"), 0).Next()
		if err == nil {
			t.Fatal("超过上限必须报错，实际没有错误")
		}
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) || appErr.Code != contracts.CodeProtocolRecordTooLarge {
			t.Fatalf("错误码应为 %s，实际 %v", contracts.CodeProtocolRecordTooLarge, err)
		}
	})

	t.Run("未以LF收尾不返回半条记录", func(t *testing.T) {
		f := NewFramer(strings.NewReader("{\"a\":1}\n{\"b\":"), 0)
		first, err := f.Next()
		if err != nil || string(first) != `{"a":1}` {
			t.Fatalf("第一条应正常返回，实际 %q / %v", first, err)
		}
		_, err = f.Next()
		if err == nil {
			t.Fatal("不完整记录必须报错，不能当作有效记录返回")
		}
		if errors.Is(err, io.EOF) {
			t.Fatal("不完整记录应报 unexpected EOF 类错误，而不是干净的 io.EOF")
		}
		// 分帧器一旦 poisoned，后续调用必须持续报错，避免错位产生假记录。
		if _, err2 := f.Next(); err2 == nil {
			t.Fatal("poisoned 后仍返回了记录")
		}
	})

	t.Run("空行返回空记录且不报错", func(t *testing.T) {
		got, err := collectFrames(t, NewFramer(strings.NewReader("\n\n{\"a\":1}\n"), 0))
		if err != nil {
			t.Fatalf("分帧失败: %v", err)
		}
		want := []string{"", "", `{"a":1}`}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("空行处理错误：期望 %#v，实际 %#v", want, got)
		}
	})
}
