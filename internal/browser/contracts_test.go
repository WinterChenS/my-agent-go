package browser

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// ---- 基线固定值 ----

// TestBaselineLimits 锁定基线 §7 的数字。
//
// 这些常量是验收口径本身：改了数字就等于改了 BR09/BR12 的通过条件。必须用一个
// 会失败的测试挡住"顺手放宽一点"的改动。
func TestBaselineLimits(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"MaxSnapshotElements", MaxSnapshotElements, 300},
		{"MaxSnapshotTextChars", MaxSnapshotTextChars, 40000},
		{"MaxSnapshotJSONBytes", MaxSnapshotJSONBytes, 256 * 1024},
		{"MaxScreenshotBytes", MaxScreenshotBytes, 4 * 1024 * 1024},
		{"MaxTabsPerSession", MaxTabsPerSession, 4},
		{"MaxTabsTotal", MaxTabsTotal, 12},
		{"ActionTimeout", int64(ActionTimeout / time.Second), 15},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d，基线要求 %d", c.name, c.got, c.want)
		}
	}
}

// ---- schema 与 Go 结构体交叉校验 ----

type schemaProp struct {
	Type         string      `json:"type"`
	Ref          string      `json:"$ref"`
	Enum         []string    `json:"enum"`
	Items        *schemaProp `json:"items"`
	MaxItems     *int        `json:"maxItems"`
	MaxLength    *int        `json:"maxLength"`
	Maximum      *float64    `json:"maximum"`
	Minimum      *float64    `json:"minimum"`
	MaxBytes     *int        `json:"x-maxBytes"`
	MaxJSONBytes *int        `json:"x-maxJsonBytes"`
}

type schemaDef struct {
	Type         string                `json:"type"`
	Required     []string              `json:"required"`
	Properties   map[string]schemaProp `json:"properties"`
	Enum         []string              `json:"enum"`
	Additional   *bool                 `json:"additionalProperties"`
	MaxItems     *int                  `json:"maxItems"`
	MaxLength    *int                  `json:"maxLength"`
	MaxBytes     *int                  `json:"x-maxBytes"`
	MaxJSONBytes *int                  `json:"x-maxJsonBytes"`
}

type schemaDoc struct {
	Defs map[string]schemaDef `json:"$defs"`
}

// dtoTypes 是"必须出现在 schema 里的 Go 类型"。
//
// 新增跨边界 DTO 时必须同时改这里和 schemas/browser.schema.json；
// TestSchemaCoversEveryExportedType 用 AST 扫描兜住"只加了 Go 类型"的漏项。
var dtoTypes = []reflect.Type{
	reflect.TypeOf(Snapshot{}),
	reflect.TypeOf(Element{}),
	reflect.TypeOf(Rect{}),
	reflect.TypeOf(Viewport{}),
	reflect.TypeOf(UnsupportedFrame{}),
	reflect.TypeOf(Action{}),
	reflect.TypeOf(ExpectedCondition{}),
	reflect.TypeOf(ActionResult{}),
	reflect.TypeOf(Verification{}),
	reflect.TypeOf(Image{}),
	reflect.TypeOf(SlotGeometry{}),
	reflect.TypeOf(TabOptions{}),
	reflect.TypeOf(ObserveOptions{}),
	reflect.TypeOf(CapabilitySet{}),
	reflect.TypeOf(UnsupportedCapability{}),
	reflect.TypeOf(ActionKind("")),
	reflect.TypeOf(ConditionKind("")),
	reflect.TypeOf(VerificationStatus("")),
}

// nonDTOTypes 是明确不进 schema 的导出类型，注释即理由。
var nonDTOTypes = map[string]string{
	"NativeParent":         "持有平台原生句柄，MarshalJSON 直接返回错误（不可序列化）",
	"BrowserBackend":       "行为接口，不是数据形状",
	"BrowserSurface":       "行为接口，不是数据形状",
	"UnimplementedBackend": "B00 占位实现，不是 DTO",
	"UnimplementedSurface": "B00 占位实现，不是 DTO",
	"SessionTabs":          "内存记账表，不跨边界",
	"TabID":                "contracts.TabID 的别名，字符串别名由 contracts 定义",
	// B04 新增：引用注册表是 backend 的内存状态（快照 id → ref → 元素本体），
	// 线上只出现 ref 字符串本身，不出现在任何 JSON 里。
	"RefRegistry": "内存中的引用注册表，不跨边界（快照 JSON 只带 ref 字符串）",
	"RefTable":    "一份快照的引用表，backend 内存状态，不跨边界",
	// ControlKind 是 fill 的处理方式分类（行为分支），不是跨边界的数据形状。
	"ControlKind": "fill 的控件分类，行为分支，不跨边界",
	// B05：控制权/串行队列/授权的类型。它们的线上契约（schemas/browser.schema.json）
	// 本轮**不允许**改（B05 的任务边界明确排除 schemas/），所以这里逐个登记理由，
	// 而不是让 TestSchemaCoversEveryExportedType 变成"随便加类型都行"。
	// 需要把它们写进 schema 时，必须一次受控变更同时改 schema 与 dtoTypes，
	// 与 B04 的 Snapshot.Truncated 是同一类欠账。
	"ControlMode":          "控制状态枚举，线上契约随 schemas/browser.schema.json 的受控变更一起落地（B05 禁止改 schemas/）",
	"ControlState":         "TakeOver/Resume 的返回值，同上：schema 待受控变更，当前只走进程内/证据通道",
	"TabInfo":              "面板与工具输出的一致性视图，同上：schema 待受控变更",
	"AuthorizationRequest": "授权请求（服务端内部形状，事实由 feature 决定而非模型），同上",
	"AuthorizationToken":   "已授权动作的凭据（含过期时间），不发给模型，schema 待受控变更",
	"TokenGuard":           "许可签发/复核的行为辅助类型，不是数据形状",
	"Authorizer":           "授权行为接口，不是数据形状",
	"Service":              "浏览器工具执行面（行为对象），不是 DTO",
	"ServiceOptions":       "Service 的构造参数，不是跨边界 DTO",
	"TimelineEntry":        "动作时间线的证据行（写 JSONL 证据），线上契约随 schema 受控变更一起落",
}

func loadSchema(t *testing.T) schemaDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "browser.schema.json"))
	if err != nil {
		t.Fatalf("读取 schemas/browser.schema.json: %v", err)
	}
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema 不是合法 JSON: %v", err)
	}
	if len(doc.Defs) == 0 {
		t.Fatal("schema 缺少 $defs")
	}
	return doc
}

// TestSchemaMatchesGoTypes 逐字段核对 schema 与 Go：字段名、required、JSON 类型、
// 引用目标。任何一边改了名字或加了字段都会在这里失败，避免"文档说一套、代码发一套"。
func TestSchemaMatchesGoTypes(t *testing.T) {
	doc := loadSchema(t)

	for _, typ := range dtoTypes {
		name := typ.Name()
		def, ok := doc.Defs[name]
		if !ok {
			t.Errorf("$defs 缺少 %s", name)
			continue
		}
		if typ.Kind() == reflect.String {
			assertEnumDef(t, name, def, stringEnumValues(typ.Name()))
			continue
		}
		if typ.Kind() != reflect.Struct {
			t.Fatalf("%s 既不是 struct 也不是 string 枚举", name)
		}
		if def.Additional == nil || *def.Additional {
			t.Errorf("%s 必须 additionalProperties=false", name)
		}

		wantProps := map[string]schemaProp{}
		wantRequired := []string{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			jsonName, optional := jsonNameOf(f)
			if jsonName == "-" || jsonName == "" {
				continue
			}
			wantProps[jsonName] = schemaProp{}
			if !optional {
				wantRequired = append(wantRequired, jsonName)
			}
		}
		gotNames := sortedKeys(def.Properties)
		if strings.Join(gotNames, ",") != strings.Join(sortedKeys(wantProps), ",") {
			t.Errorf("%s 字段名不一致: schema=%v Go=%v", name, gotNames, sortedKeys(wantProps))
		}
		gotRequired := append([]string(nil), def.Required...)
		sort.Strings(gotRequired)
		sort.Strings(wantRequired)
		if strings.Join(gotRequired, ",") != strings.Join(wantRequired, ",") {
			t.Errorf("%s required 不一致: schema=%v Go(非 omitempty)=%v", name, gotRequired, wantRequired)
		}

		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			jsonName, _ := jsonNameOf(f)
			prop, ok := def.Properties[jsonName]
			if !ok {
				continue
			}
			checkPropType(t, name+"."+jsonName, f.Type, prop)
		}
	}
}

func assertEnumDef(t *testing.T, name string, def schemaDef, want []string) {
	t.Helper()
	if def.Type != "string" {
		t.Errorf("%s 的 schema type 应为 string，实际 %q", name, def.Type)
	}
	if strings.Join(def.Enum, ",") != strings.Join(want, ",") {
		t.Errorf("%s 枚举不一致: schema=%v Go=%v", name, def.Enum, want)
	}
}

func stringEnumValues(typeName string) []string {
	var out []string
	switch typeName {
	case "ActionKind":
		for _, v := range AllActionKinds {
			out = append(out, string(v))
		}
	case "ConditionKind":
		for _, v := range AllConditionKinds {
			out = append(out, string(v))
		}
	case "VerificationStatus":
		for _, v := range AllVerificationStatuses {
			out = append(out, string(v))
		}
	}
	return out
}

func checkPropType(t *testing.T, path string, goType reflect.Type, prop schemaProp) {
	t.Helper()
	if goType.Kind() == reflect.Pointer || goType.Kind() == reflect.Slice ||
		goType.Kind() == reflect.Map || goType.Kind() == reflect.Array {
		switch goType.Kind() {
		case reflect.Pointer:
			checkPropType(t, path, goType.Elem(), prop)
			return
		case reflect.Slice, reflect.Array:
			if goType.Elem().Kind() == reflect.Uint8 { // []byte → base64 string
				if prop.Type != "string" {
					t.Errorf("%s 是 []byte，schema 应为 base64 string，实际 %q", path, prop.Type)
				}
				return
			}
			if prop.Type != "array" {
				t.Errorf("%s 是切片，schema 应为 array，实际 %q", path, prop.Type)
				return
			}
			if prop.Items == nil {
				t.Errorf("%s 缺少 items", path)
				return
			}
			checkPropType(t, path+"[]", goType.Elem(), *prop.Items)
			return
		case reflect.Map:
			if prop.Type != "object" {
				t.Errorf("%s 是 map，schema 应为 object，实际 %q", path, prop.Type)
			}
			return
		}
	}
	if goType.Kind() == reflect.Struct || (goType.Kind() == reflect.String && len(stringEnumValues(goType.Name())) > 0) {
		want := "#/$defs/" + goType.Name()
		if prop.Ref != want {
			t.Errorf("%s 应引用 %s，实际 %q", path, want, prop.Ref)
		}
		return
	}
	want := jsonTypeOf(goType)
	if prop.Type != want && !(want == "integer" && prop.Type == "number") {
		t.Errorf("%s 的 schema type=%q，Go 对应 %q", path, prop.Type, want)
	}
}

func jsonTypeOf(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	}
	return t.Kind().String()
}

// jsonNameOf 解析 json tag，返回线上字段名与"是否可省略"。
func jsonNameOf(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = f.Name
	}
	optional := false
	for _, opt := range parts[1:] {
		if opt == "omitempty" || opt == "omitzero" {
			optional = true
		}
	}
	return name, optional
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSchemaLimitsMatchConstants 把 schema 里的数字上限绑到基线常量上。
//
// 分别在 schema 里写 300 和在这里比较是必要的：两处都写死反而没人发现它们漂移。
func TestSchemaLimitsMatchConstants(t *testing.T) {
	doc := loadSchema(t)

	snap := doc.Defs["Snapshot"]
	if snap.MaxJSONBytes == nil || *snap.MaxJSONBytes != MaxSnapshotJSONBytes {
		t.Errorf("Snapshot x-maxJsonBytes 与 MaxSnapshotJSONBytes=%d 不一致", MaxSnapshotJSONBytes)
	}
	els := snap.Properties["elements"]
	if els.MaxItems == nil || *els.MaxItems != MaxSnapshotElements {
		t.Errorf("Snapshot.elements maxItems 与 MaxSnapshotElements=%d 不一致", MaxSnapshotElements)
	}
	text := snap.Properties["text"]
	if text.MaxLength == nil || *text.MaxLength != MaxSnapshotTextChars {
		t.Errorf("Snapshot.text maxLength 与 MaxSnapshotTextChars=%d 不一致", MaxSnapshotTextChars)
	}

	img := doc.Defs["Image"]
	if img.MaxBytes == nil || *img.MaxBytes != MaxScreenshotBytes {
		t.Errorf("Image x-maxBytes 与 MaxScreenshotBytes=%d 不一致", MaxScreenshotBytes)
	}
	if b := img.Properties["bytes"]; b.MaxBytes == nil || *b.MaxBytes != MaxScreenshotBytes {
		t.Errorf("Image.bytes x-maxBytes 与 MaxScreenshotBytes=%d 不一致", MaxScreenshotBytes)
	}

	cond := doc.Defs["ExpectedCondition"].Properties["timeoutMs"]
	wantTimeout := float64(ActionTimeout / time.Millisecond)
	if cond.Maximum == nil || *cond.Maximum != wantTimeout {
		t.Errorf("ExpectedCondition.timeoutMs maximum 与 ActionTimeout=%s 不一致", ActionTimeout)
	}

	obs := doc.Defs["ObserveOptions"].Properties
	if v := obs["maxElements"].Maximum; v == nil || *v != float64(MaxSnapshotElements) {
		t.Errorf("ObserveOptions.maxElements maximum 与 MaxSnapshotElements 不一致")
	}
	if v := obs["maxTextChars"].Maximum; v == nil || *v != float64(MaxSnapshotTextChars) {
		t.Errorf("ObserveOptions.maxTextChars maximum 与 MaxSnapshotTextChars 不一致")
	}
}

// TestSchemaCoversEveryExportedType 用 AST 扫本包的导出类型声明。
//
// 目的只有一个：以后有人往 types.go 加一个 DTO 却忘了写 schema，这里会失败，
// 而不是等到前端/模型拿到一个没有契约的字段。
func TestSchemaCoversEveryExportedType(t *testing.T) {
	doc := loadSchema(t)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("解析本包源码: %v", err)
	}
	pkg, ok := pkgs["browser"]
	if !ok {
		t.Fatalf("找不到包 browser，实际 %v", sortedKeys(pkgs))
	}

	declared := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ast.IsExported(ts.Name.Name) {
					continue
				}
				// 只看数据形状：struct 与具名 string（枚举）。别名用 = 定义，跳过。
				switch ts.Type.(type) {
				case *ast.StructType:
					declared[ts.Name.Name] = true
				case *ast.Ident:
					if ts.Type.(*ast.Ident).Name == "string" {
						declared[ts.Name.Name] = true
					}
				}
			}
		}
	}

	for name := range declared {
		if _, ok := doc.Defs[name]; ok {
			continue
		}
		if _, ok := nonDTOTypes[name]; ok {
			continue
		}
		t.Errorf("导出类型 %s 既不在 schema $defs 里，也没有登记进 nonDTOTypes（理由必填）", name)
	}
	for name := range doc.Defs {
		if !declared[name] {
			t.Errorf("schema 声明了 $defs/%s，但本包没有对应的导出类型", name)
		}
	}
	for _, typ := range dtoTypes {
		if !declared[typ.Name()] {
			t.Errorf("dtoTypes 里的 %s 在本包源码里找不到声明", typ.Name())
		}
	}
}

// ---- 不变量 ----

// TestNativeParentNeverSerializes 是"原生句柄不进 JSON"的可执行版本。
func TestNativeParentNeverSerializes(t *testing.T) {
	parent := NewNativeParent(0xDEADBEEF, "wails-main-window")
	if !parent.Valid() {
		t.Fatal("带句柄的 NativeParent 应判为 valid")
	}
	if strings.Contains(parent.String(), "DEAD") || strings.Contains(parent.String(), "3735928559") {
		t.Fatalf("String() 泄漏了句柄值: %q", parent.String())
	}
	if _, err := json.Marshal(parent); !errors.Is(err, ErrNativeParentNotSerializable) {
		t.Fatalf("直接序列化 NativeParent 应失败，实际 err=%v", err)
	}
	// 嵌进 DTO 也必须失败：否则事件/日志里会漏出一串指针。
	wrapped := struct {
		Slot  string       `json:"slot"`
		Owner NativeParent `json:"owner"`
	}{Slot: "right", Owner: parent}
	if _, err := json.Marshal(wrapped); !errors.Is(err, ErrNativeParentNotSerializable) {
		t.Fatalf("嵌套序列化 NativeParent 应失败，实际 err=%v", err)
	}
	if NewNativeParent(nil, "none").Valid() {
		t.Fatal("nil 句柄不应判为 valid")
	}
}

// TestExpectedConditionRejectsArbitraryJS 守住"只允许有界检查"。
func TestExpectedConditionRejectsArbitraryJS(t *testing.T) {
	for _, kind := range []string{"evaluate", "script", "js", "eval", "wait_for_function"} {
		err := ExpectedCondition{Kind: ConditionKind(kind)}.Validate()
		if err == nil {
			t.Errorf("后置条件 %q 必须被拒绝（不允许任意 JS）", kind)
		}
		if code := appErrCode(t, err); code != contracts.CodeUnsupported {
			t.Errorf("%q 的错误码应为 UNSUPPORTED，实际 %q", kind, code)
		}
	}
	// 有界检查必须合法。
	ok := []ExpectedCondition{
		{Kind: ConditionURLEquals, URL: "http://127.0.0.1:1234/form"},
		{Kind: ConditionElementText, Ref: "e1", Value: "已提交"},
		{Kind: ConditionFieldValue, Ref: "e2", Value: "张三"},
		{Kind: ConditionElementPresent, Ref: "e3"},
		{},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("合法后置条件 %+v 被拒绝: %v", c, err)
		}
	}
	// 超时不得超过动作超时。
	if err := (ExpectedCondition{Kind: ConditionElementPresent, Ref: "e1",
		TimeoutMS: int(ActionTimeout/time.Millisecond) + 1}).Validate(); err == nil {
		t.Error("后置条件超时超过 15s 必须被拒绝")
	}
	// 没有 kind 却带 url/ref/value 容易被误读成"核验过"，必须拒绝。
	if err := (ExpectedCondition{URL: "http://x"}).Validate(); err == nil {
		t.Error("无 kind 却带 url 必须被拒绝")
	}
}

func TestActionValidate(t *testing.T) {
	cases := []struct {
		name string
		act  Action
		ok   bool
		code string
	}{
		{"click 正常", Action{Kind: ActionClick, SnapshotID: "s1", Ref: "e1"}, true, ""},
		{"click 缺 ref", Action{Kind: ActionClick, SnapshotID: "s1"}, false, contracts.CodeStaleSnapshot},
		{"缺 snapshotId", Action{Kind: ActionClick, Ref: "e1"}, false, contracts.CodeStaleSnapshot},
		{"fill 正常", Action{Kind: ActionFill, SnapshotID: "s1", Ref: "e2", Text: "x"}, true, ""},
		{"key 正常", Action{Kind: ActionKey, SnapshotID: "s1", Key: "Enter"}, true, ""},
		{"key 缺 key", Action{Kind: ActionKey, SnapshotID: "s1"}, false, contracts.CodeUnsupported},
		{"scroll 正常", Action{Kind: ActionScroll, SnapshotID: "s1", DeltaY: 400}, true, ""},
		{"scroll 零位移", Action{Kind: ActionScroll, SnapshotID: "s1"}, false, contracts.CodeUnsupported},
		{"未知动作", Action{Kind: "dblclick", SnapshotID: "s1", Ref: "e1"}, false, contracts.CodeUnsupported},
	}
	for _, c := range cases {
		err := c.act.Validate()
		if c.ok {
			if err != nil {
				t.Errorf("%s: 应通过，实际 %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: 应被拒绝", c.name)
			continue
		}
		if code := appErrCode(t, err); code != c.code {
			t.Errorf("%s: 错误码应为 %q，实际 %q", c.name, c.code, code)
		}
	}
}

// TestActionResultDeliveredIsNotVerified 是 BR10 的核心不变量。
func TestActionResultDeliveredIsNotVerified(t *testing.T) {
	r := DeliveredResult("nav-1")
	if !r.Delivered || r.Verified {
		t.Fatalf("DeliveredResult 必须是 delivered=true verified=false，实际 %+v", r)
	}
	if r.Verification.Status != VerificationUnknown {
		t.Fatalf("没有后置条件时状态必须是 unknown，实际 %q", r.Verification.Status)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("DeliveredResult 必须自洽: %v", err)
	}

	bad := []ActionResult{
		{Delivered: true, Verified: true, NavigationID: "nav-1"},
		{Delivered: true, Verified: true, NavigationID: "nav-1",
			Verification: Verification{Status: VerificationUnknown}},
		{Delivered: true, Verified: false, NavigationID: "nav-1",
			Verification: Verification{Status: VerificationVerified}},
		{Delivered: true, NavigationID: "nav-1",
			Verification: Verification{Condition: ConditionElementPresent, Status: "maybe"}},
	}
	for i, b := range bad {
		if err := b.Validate(); err == nil {
			t.Errorf("第 %d 条不自洽的 ActionResult 应被拒绝: %+v", i, b)
		}
	}

	good := ActionResult{Delivered: true, Verified: true, NavigationID: "nav-2",
		Verification: Verification{Condition: ConditionElementPresent, Status: VerificationVerified}}
	if err := good.Validate(); err != nil {
		t.Fatalf("满足条件的核验应通过: %v", err)
	}
}

// TestSnapshotLimits 验证三条上限在边界上真的生效。
func TestSnapshotLimits(t *testing.T) {
	mk := func(n int) Snapshot {
		els := make([]Element, n)
		for i := range els {
			els[i] = Element{Ref: "e" + itoa(i), Tag: "button", Role: "button", Name: "b"}
		}
		return Snapshot{ID: "s", TabID: "t", NavigationID: "n", Elements: els}
	}
	if err := mk(MaxSnapshotElements).CheckLimits(); err != nil {
		t.Fatalf("恰好 %d 个元素应通过: %v", MaxSnapshotElements, err)
	}
	if err := mk(MaxSnapshotElements + 1).CheckLimits(); err == nil {
		t.Fatalf("%d 个元素必须超限", MaxSnapshotElements+1)
	}

	base := Snapshot{ID: "s", TabID: "t", NavigationID: "n"}
	okText := base
	okText.Text = strings.Repeat("字", MaxSnapshotTextChars)
	if err := okText.CheckLimits(); err != nil {
		t.Fatalf("恰好 %d 字符应通过: %v", MaxSnapshotTextChars, err)
	}
	tooLong := base
	tooLong.Text = strings.Repeat("字", MaxSnapshotTextChars+1)
	if err := tooLong.CheckLimits(); err == nil {
		t.Fatal("超过 40000 字符必须被拒绝")
	} else if !strings.Contains(err.Error(), "字符") {
		t.Fatalf("错误消息应说明字符数超限: %v", err)
	}
	// 字符不是字节：40000 个中文字符是 120000 字节，不能按字节判定。
	if n := utf8.RuneCountInString(okText.Text); n != MaxSnapshotTextChars {
		t.Fatalf("测试前提错误：期望 %d 字符，实际 %d", MaxSnapshotTextChars, n)
	}

	// JSON 上限：用大量元素把序列化体积推过 256KiB。
	big := mk(MaxSnapshotElements)
	for i := range big.Elements {
		big.Elements[i].Name = strings.Repeat("n", 1000)
	}
	if err := big.CheckLimits(); err == nil {
		t.Fatal("序列化后超过 256KiB 必须被拒绝")
	}
}

func TestSnapshotValidateRefs(t *testing.T) {
	dup := Snapshot{Elements: []Element{{Ref: "e1"}, {Ref: "e1"}}}
	if err := dup.ValidateRefs(); err == nil {
		t.Fatal("重复 ref 必须被拒绝")
	}
	empty := Snapshot{Elements: []Element{{Ref: ""}}}
	if err := empty.ValidateRefs(); err == nil {
		t.Fatal("空 ref 必须被拒绝")
	}
	ok := Snapshot{Elements: []Element{{Ref: "e1"}, {Ref: "e2"}}}
	if err := ok.ValidateRefs(); err != nil {
		t.Fatalf("唯一 ref 应通过: %v", err)
	}
}

func TestImageValidate(t *testing.T) {
	good := Image{
		Bytes: []byte("png"), MimeType: "image/png",
		PixelWidth: 10, PixelHeight: 10,
		CSSWidth: 10, CSSHeight: 10, Scale: 1, NavigationID: "nav-1",
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("合法截图应通过: %v", err)
	}

	oversize := good
	oversize.Bytes = make([]byte, MaxScreenshotBytes+1)
	if err := oversize.Validate(); err == nil {
		t.Fatal("超过 4MiB 的截图必须被拒绝")
	}
	exact := good
	exact.Bytes = make([]byte, MaxScreenshotBytes)
	if err := exact.Validate(); err != nil {
		t.Fatalf("恰好 4MiB 应通过: %v", err)
	}

	for name, mutate := range map[string]func(i *Image){
		"空内容":     func(i *Image) { i.Bytes = nil },
		"未知 mime": func(i *Image) { i.MimeType = "image/gif" },
		"缺像素宽":    func(i *Image) { i.PixelWidth = 0 },
		"缺 CSS 宽": func(i *Image) { i.CSSWidth = 0 },
		"缺 scale": func(i *Image) { i.Scale = 0 },
		"缺 navID": func(i *Image) { i.NavigationID = "" },
	} {
		bad := good
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

func TestSlotGeometryValidate(t *testing.T) {
	ok := SlotGeometry{SlotID: "right", Revision: 3, Width: 400, Height: 800, Zoom: 1}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法槽位应通过: %v", err)
	}
	for name, bad := range map[string]SlotGeometry{
		"缺 slotId": {},
		"负宽度":      {SlotID: "right", Width: -1, Zoom: 1},
		"零 zoom":   {SlotID: "right", Zoom: 0},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

func TestTabLimit(t *testing.T) {
	if err := CheckTabLimit(0, 0); err != nil {
		t.Fatalf("空表应允许：%v", err)
	}
	if err := CheckTabLimit(MaxTabsPerSession-1, MaxTabsTotal-1); err != nil {
		t.Fatalf("未到上限应允许：%v", err)
	}
	if err := CheckTabLimit(MaxTabsPerSession, 4); appErrCode(t, err) != CodeTabLimitExceeded {
		t.Fatalf("第 5 个 tab 必须返回 %s，实际 %v", CodeTabLimitExceeded, err)
	}
	if err := CheckTabLimit(1, MaxTabsTotal); appErrCode(t, err) != CodeTabLimitExceeded {
		t.Fatalf("第 13 个 tab 必须返回 %s，实际 %v", CodeTabLimitExceeded, err)
	}

	tabs := NewSessionTabs()
	for i := 0; i < MaxTabsPerSession; i++ {
		if err := tabs.Reserve("sess-a"); err != nil {
			t.Fatalf("第 %d 个 tab 应允许: %v", i+1, err)
		}
	}
	if err := tabs.Reserve("sess-a"); appErrCode(t, err) != CodeTabLimitExceeded {
		t.Fatalf("session 内第 5 个 tab 必须被拒绝，实际 %v", err)
	}
	if got := tabs.CountFor("sess-a"); got != MaxTabsPerSession {
		t.Fatalf("被拒绝后计数不应增长，实际 %d", got)
	}
	tabs.Release("sess-a")
	if err := tabs.Reserve("sess-a"); err != nil {
		t.Fatalf("释放后应能再占一个: %v", err)
	}

	total := NewSessionTabs()
	for s := 0; s < MaxTabsTotal/MaxTabsPerSession; s++ {
		session := contracts.SessionID("sess-" + itoa(s))
		for i := 0; i < MaxTabsPerSession; i++ {
			if err := total.Reserve(session); err != nil {
				t.Fatalf("总计第 %d 个 tab 应允许: %v", total.Total()+1, err)
			}
		}
	}
	if err := total.Reserve("sess-new"); appErrCode(t, err) != CodeTabLimitExceeded {
		t.Fatalf("总计第 13 个 tab 必须被拒绝，实际 %v", err)
	}
}

func TestObserveOptionsClamp(t *testing.T) {
	if got := (ObserveOptions{}).EffectiveMaxElements(); got != MaxSnapshotElements {
		t.Fatalf("0 应取基线默认 %d，实际 %d", MaxSnapshotElements, got)
	}
	if got := (ObserveOptions{MaxElements: 1000}).EffectiveMaxElements(); got != MaxSnapshotElements {
		t.Fatalf("超过基线必须被夹到 %d，实际 %d", MaxSnapshotElements, got)
	}
	if got := (ObserveOptions{MaxElements: 50}).EffectiveMaxElements(); got != 50 {
		t.Fatalf("下调应生效，实际 %d", got)
	}
	if got := (ObserveOptions{MaxTextChars: MaxSnapshotTextChars + 1}).EffectiveMaxTextChars(); got != MaxSnapshotTextChars {
		t.Fatalf("文本上限必须被夹到 %d，实际 %d", MaxSnapshotTextChars, got)
	}
}

// TestUnimplementedReturnsNotImplemented 是 B00 的"初始失败"契约本身：
// 没有实现就必须报错，不能返回零值让调用方以为成功。
func TestUnimplementedReturnsNotImplemented(t *testing.T) {
	ctx := context.Background()
	var backend BrowserBackend = UnimplementedBackend{Reason: "B00 只做契约"}
	var surface BrowserSurface = UnimplementedSurface{Reason: "B00 只做契约"}

	caps := backend.Capabilities()
	if caps.Navigate || caps.Observe || caps.Act || caps.Screenshot {
		t.Fatalf("未实现时四项主能力必须为 false，实际 %+v", caps)
	}
	if len(caps.Unsupported) == 0 {
		t.Fatal("未实现时必须逐项给出不可用原因")
	}

	checks := map[string]error{
		"CreateTab": func() error {
			tid, err := backend.CreateTab(ctx, TabOptions{URL: "http://127.0.0.1:1/"})
			if tid != "" {
				t.Errorf("CreateTab 失败时不应返回 tabId，实际 %q", tid)
			}
			return err
		}(),
		"Navigate": func() error { return backend.Navigate(ctx, "t1", "http://127.0.0.1:1/") }(),
		"Observe": func() error {
			s, err := backend.Observe(ctx, "t1", ObserveOptions{})
			if s.ID != "" || len(s.Elements) != 0 {
				t.Errorf("Observe 失败时不应返回快照，实际 %+v", s)
			}
			return err
		}(),
		"Act": func() error {
			r, err := backend.Act(ctx, "t1", Action{Kind: ActionClick, SnapshotID: "s1", Ref: "e1"})
			if r.Delivered {
				t.Error("未实现时不得声称输入已发出")
			}
			return err
		}(),
		"Screenshot": func() error {
			img, err := backend.Screenshot(ctx, "t1")
			if len(img.Bytes) != 0 {
				t.Error("未实现时不应返回截图内容")
			}
			return err
		}(),
		"CloseTab": func() error { return backend.CloseTab(ctx, "t1") }(),
	}
	for name, err := range checks {
		if code := appErrCode(t, err); code != CodeNotImplemented {
			t.Errorf("%s 应返回 %s，实际 code=%q err=%v", name, CodeNotImplemented, code, err)
		}
	}

	parent := NewNativeParent(1, "wails-main-window")
	if err := surface.Attach(ctx, parent); appErrCode(t, err) != CodeNotImplemented {
		t.Errorf("Attach 应返回 %s，实际 %v", CodeNotImplemented, err)
	}
	if err := surface.Present(ctx, "t1", SlotGeometry{SlotID: "right", Revision: 1, Width: 1, Height: 1, Zoom: 1}); appErrCode(t, err) != CodeNotImplemented {
		t.Errorf("Present 应返回 %s，实际 %v", CodeNotImplemented, err)
	}
	if err := surface.SetVisible(ctx, true); appErrCode(t, err) != CodeNotImplemented {
		t.Errorf("SetVisible 应返回 %s，实际 %v", CodeNotImplemented, err)
	}
	if err := surface.Detach(ctx); appErrCode(t, err) != CodeNotImplemented {
		t.Errorf("Detach 应返回 %s，实际 %v", CodeNotImplemented, err)
	}
}

// TestWireFormatGolden 固定线上字段名，避免改名悄悄破坏前端/模型侧契约。
func TestWireFormatGolden(t *testing.T) {
	snap := Snapshot{
		ID: "snap-1", TabID: "tab-1", NavigationID: "nav-1", ControlGeneration: 2,
		Viewport: Viewport{Width: 1024, Height: 720, Scale: 1.5},
		Elements: []Element{{
			Ref: "e1", FrameID: "", Role: "button", Name: "提交", Tag: "button",
			Disabled: false, Bounds: Rect{X: 1, Y: 2, Width: 3, Height: 4},
		}},
		Text:              "示例",
		UnsupportedFrames: []UnsupportedFrame{{FrameID: "f1", URL: "http://b/frame", Reason: "cross-origin"}},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"id", "tabId", "navigationId", "controlGeneration",
		"viewport", "elements", "text", "unsupportedFrames"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("snapshot 缺少线上字段 %q: %s", want, raw)
		}
	}

	// 没有后置条件的动作必须省略 expectedCondition（结构化省略，不是写成 null）。
	action := Action{Kind: ActionClick, SnapshotID: "snap-1", Ref: "e1"}
	rawAction, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	if strings.Contains(string(rawAction), "expectedCondition") {
		t.Fatalf("没有后置条件时不应出现 expectedCondition 字段: %s", rawAction)
	}

	// 有后置条件时必须出现，且只含允许的 kind。
	action.ExpectedCondition = ExpectedCondition{Kind: ConditionElementPresent, Ref: "e1"}
	rawAction, err = json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	if !strings.Contains(string(rawAction), `"expectedCondition"`) {
		t.Fatalf("有后置条件时必须序列化: %s", rawAction)
	}

	// ActionResult 的 unknown 状态必须能原样往返。
	rawResult, err := json.Marshal(DeliveredResult("nav-1"))
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var back ActionResult
	if err := json.Unmarshal(rawResult, &back); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if back.Verified || back.Verification.Status != VerificationUnknown {
		t.Fatalf("往返后 verified/status 被改变: %+v", back)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("往返后应仍自洽: %v", err)
	}
}

// ---- 小工具 ----

func appErrCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误，实际 nil")
	}
	var ae *contracts.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("错误必须是 *contracts.AppError（前端按 Code 分支），实际 %T: %v", err, err)
	}
	return ae.Code
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
