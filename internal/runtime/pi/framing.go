package pi

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"my-agent-go/internal/contracts"
)

// MaxRecordBytes 是单条 RPC 记录的长度上限，对应基线 §6「单 record 最大 16 MiB」。
// 超过即报 ProtocolRecordTooLarge 并标记该 run 失败；绝不截断 JSON 后当作成功。
const MaxRecordBytes = 16 << 20

// Framer 按 LF 对 RPC 字节流分帧。
//
// 分帧规则（基线 §6）：
//   - 只有 LF（0x0A）是记录分隔符；U+2028 / U+2029 是合法的 JSON 字符串内容，不能断行
//   - 按字节处理，因此多字节 UTF-8 字符被 chunk 边界切开也不影响还原
//   - CRLF 的尾部 CR 在返回前剥离
//
// 返回的切片是调用方独有的拷贝，可以安全长期持有（bufio 的内部缓冲会被下次读取覆盖）。
type Framer struct {
	r        *bufio.Reader
	maxBytes int
	buf      []byte
	poisoned error
}

// NewFramer 创建一个分帧器。maxBytes<=0 时使用 MaxRecordBytes。
func NewFramer(r io.Reader, maxBytes int) *Framer {
	if maxBytes <= 0 {
		maxBytes = MaxRecordBytes
	}
	return &Framer{
		r:        bufio.NewReaderSize(r, 64<<10),
		maxBytes: maxBytes,
	}
}

// Next 返回下一条记录（不含行尾 LF/CR）。流正常结束返回 io.EOF。
//
// 一旦返回错误，分帧器进入 poisoned 状态，后续调用返回同一个错误：
// 流已经失去记录边界，继续读下去只会产生错位的假记录。
func (f *Framer) Next() ([]byte, error) {
	if f.poisoned != nil {
		return nil, f.poisoned
	}
	f.buf = f.buf[:0]

	for {
		chunk, err := f.r.ReadSlice('\n')
		if len(chunk) > 0 {
			hasLF := chunk[len(chunk)-1] == '\n'
			content := chunk
			if hasLF {
				content = chunk[:len(chunk)-1]
			}
			// 在累积过程中就校验上限：对端一直不发送 LF 时不能无限吃内存。
			if len(f.buf)+len(content) > f.maxBytes {
				f.poisoned = recordTooLargeError(len(f.buf)+len(content), f.maxBytes)
				return nil, f.poisoned
			}
			f.buf = append(f.buf, content...)
			if hasLF {
				return f.finish(), nil
			}
		}

		switch {
		case err == nil:
			// ReadSlice 无错时会带上分隔符，走到这里说明分隔符在上一轮已消费；继续读。
			continue
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(f.buf) > 0 {
				// 不完整的尾部数据不能当作有效记录返回：LF 分帧语义要求以 LF 收尾。
				f.poisoned = fmt.Errorf("rpc 流在记录中途结束（最后 %d 字节未以 LF 收尾）: %w",
					len(f.buf), io.ErrUnexpectedEOF)
				return nil, f.poisoned
			}
			return nil, io.EOF
		default:
			f.poisoned = err
			return nil, err
		}
	}
}

// finish 复制当前累积内容作为最终记录，并剥离 CRLF 的 CR。
func (f *Framer) finish() []byte {
	out := f.buf
	if n := len(out); n > 0 && out[n-1] == '\r' {
		out = out[:n-1]
	}
	if len(out) == 0 {
		return []byte{}
	}
	return bytes.Clone(out)
}

// recordTooLargeError 构造统一错误码的"记录过大"错误。
// Retryable=false：同一条命令重发只会再触发一次上限，重试没有意义。
func recordTooLargeError(size, limit int) error {
	return contracts.NewAppError(
		contracts.CodeProtocolRecordTooLarge,
		fmt.Sprintf("rpc 记录 %d 字节超过上限 %d 字节；不截断 JSON，该记录作废", size, limit),
		false,
	)
}
