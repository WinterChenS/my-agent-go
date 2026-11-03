// Package config 实现版本化配置的权威存储（CAS）与五层合并编译（基线 §5 / F07）。
//
// 权威边界：用户级/工作区/Profile 配置以本包的版本化 JSON 为权威；SQLite 只做
// workspace/session/run 等索引，不构成第二份配置源（DB03）。
//
// 写入纪律：所有写入必须 CompareAndSwap(expectedRevision)，外部改动与并发写入
// 一律返回 ConfigConflict，绝不覆盖源文件。
package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"my-agent-go/internal/contracts"
)

// ConfigSchemaVersion 是配置 JSON 的 schemaVersion（基线 §5：schemaVersion=1）。
const ConfigSchemaVersion = 1

// Store 是按 key 存放版本化配置文档的目录型存储。
//
// 并发语义：进程内按 key 串行化（同 key 的两个 writer 只有一个能成功 CAS），
// 跨进程并发依赖文件系统语义，尚未加 OS 文件锁——该限制记录在证据里，
// 不用"看起来没冲突"冒充已覆盖。
type Store struct {
	root  string
	locks sync.Map // key(string) -> *sync.Mutex
}

// NewStore 在 root 下创建/打开配置存储。
func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "配置存储根目录为空", false)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建配置存储根目录失败: "+err.Error(), false)
	}
	return &Store{root: root}, nil
}

// Root 返回存储根目录。
func (s *Store) Root() string { return s.root }

// envelope 是磁盘上的存储信封：在权威文档之外附带 checksum，
// 用来识别"文件被外部编辑但 revision 未变"的情况。
type envelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Revision      uint64          `json:"revision"`
	Body          json.RawMessage `json:"body"`
	Checksum      string          `json:"checksum"`
}

func (s *Store) lockFor(key string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// keyPath 把逻辑 key 映射为文件路径，并拒绝路径穿越。
func (s *Store) keyPath(key string) (string, error) {
	if key == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "配置 key 为空", false)
	}
	if strings.ContainsAny(key, `/\`) || key == "." || key == ".." || strings.Contains(key, "..") {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "非法配置 key: "+key, false)
	}
	for _, r := range key {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return "", contracts.NewAppError(contracts.CodeUnsupported, "配置 key 含非法字符: "+key, false)
		}
	}
	return filepath.Join(s.root, key+".json"), nil
}

// Load 读取当前文档。文档不存在时返回 revision=0 的空文档（便于首次 CAS(0) 写入）。
//
// checksum 不匹配说明文件被绕过本存储改动过：返回 ConfigConflict，
// 不把内容当作有效配置继续使用。
func (s *Store) Load(ctx context.Context, key string) (contracts.ConfigDocument, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConfigDocument{}, err
	}
	path, err := s.keyPath(key)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	env, err := readEnvelope(path)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	if env == nil {
		return contracts.ConfigDocument{SchemaVersion: ConfigSchemaVersion, Revision: 0}, nil
	}
	if env.Checksum != checksumOf(env.Revision, env.Body) {
		return contracts.ConfigDocument{}, contracts.NewAppError(
			contracts.CodeConfigConflict,
			"配置 "+key+" 被外部编辑（checksum 不匹配），拒绝加载；请重新导入或人工确认",
			false,
		)
	}
	return contracts.ConfigDocument{SchemaVersion: env.SchemaVersion, Revision: env.Revision, Body: env.Body}, nil
}

// CompareAndSwap 在 expected 等于当前 revision 时写入新 body，revision+1。
//
// 拒绝条件（均返回 ConfigConflict，源文件保持不变）：
//   - 当前 revision 与 expected 不一致（并发 writer 或过期写者）
//   - 磁盘文件 checksum 与内容不匹配（外部编辑）
func (s *Store) CompareAndSwap(ctx context.Context, key string, expected uint64, body json.RawMessage) (contracts.ConfigDocument, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ConfigDocument{}, err
	}
	path, err := s.keyPath(key)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	normalized, err := normalizeBody(body)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}

	mu := s.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	env, err := readEnvelope(path)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	var current uint64
	if env != nil {
		if env.Checksum != checksumOf(env.Revision, env.Body) {
			return contracts.ConfigDocument{}, contracts.NewAppError(
				contracts.CodeConfigConflict,
				"配置 "+key+" 被外部编辑（checksum 不匹配），拒绝覆盖",
				false,
			)
		}
		current = env.Revision
	}
	if current != expected {
		return contracts.ConfigDocument{}, contracts.NewAppError(
			contracts.CodeConfigConflict,
			"配置 "+key+" revision 冲突：期望 "+strconv.FormatUint(expected, 10)+
				"，实际 "+strconv.FormatUint(current, 10),
			false,
		)
	}

	next := current + 1
	nextEnv := envelope{
		SchemaVersion: ConfigSchemaVersion,
		Revision:      next,
		Body:          normalized,
		Checksum:      checksumOf(next, normalized),
	}
	data, err := json.Marshal(nextEnv)
	if err != nil {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeInternal, "序列化配置失败: "+err.Error(), false)
	}
	if err := writeAtomic(path, data); err != nil {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeInternal, "写入配置失败: "+err.Error(), false)
	}
	return contracts.ConfigDocument{SchemaVersion: nextEnv.SchemaVersion, Revision: next, Body: normalized}, nil
}

// readEnvelope 读取并解析信封；文件不存在返回 (nil, nil)。
func readEnvelope(path string) (*envelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取配置失败: "+err.Error(), false)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		// 文件存在但不是合法信封：同样属于外部改动，不能当成空配置覆盖掉。
		return nil, contracts.NewAppError(contracts.CodeConfigConflict, "配置文件不是合法信封，拒绝解析: "+err.Error(), false)
	}
	if env.SchemaVersion != ConfigSchemaVersion {
		return nil, contracts.NewAppError(
			contracts.CodeUnsupported,
			"配置 schemaVersion 不受支持: "+strconv.Itoa(env.SchemaVersion),
			false,
		)
	}
	return &env, nil
}

// writeAtomic 写临时文件 → fsync → rename。崩溃只会留下 .tmp 残片，
// 主文件保持上一个完整版本（CFG04）。
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// normalizeBody 校验 body 是合法 JSON 且 schemaVersion 匹配，并压缩为规范形式。
func normalizeBody(body json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "配置 body 为空", false)
	}
	if !json.Valid(body) {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "配置 body 不是合法 JSON", false)
	}
	var probe struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "配置 body 解析失败: "+err.Error(), false)
	}
	if probe.SchemaVersion == nil {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "配置 body 缺少 schemaVersion", false)
	}
	if *probe.SchemaVersion != ConfigSchemaVersion {
		return nil, contracts.NewAppError(
			contracts.CodeUnsupported,
			"配置 schemaVersion 不受支持: "+strconv.Itoa(*probe.SchemaVersion),
			false,
		)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "配置 body 压缩失败: "+err.Error(), false)
	}
	return json.RawMessage(buf.Bytes()), nil
}

// checksumOf 绑定 revision 与规范化后的 body，用于识别绕过本存储的编辑。
func checksumOf(revision uint64, body json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte("myagent/config/v1"))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatUint(revision, 10)))
	h.Write([]byte{0})
	h.Write(canonicalize(body))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// canonicalize 去掉 JSON 空白（不改变语义），使空白调整不算外部编辑。
func canonicalize(body json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return body
	}
	return buf.Bytes()
}
