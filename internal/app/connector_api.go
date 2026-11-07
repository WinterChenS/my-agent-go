package app

// C08a 的绑定层：连接器的列表/保存/连接/撤销/状态/健康，以及把待审批请求
// 交给 UI。
//
// 纪律：
//   - 这里不出现任何凭证材料：入参的密码只往 OS 凭证库里放一次，出参只有
//     credential ref 与 health；
//   - 定义落 <appRoot>/connectors.db（迁移 003_connectors.sql），审计也在这里，
//     且审计是追加式的；
//   - 健康状态只来自真实认证调用：没有凭证就是 blocked，绝不显示 connected。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/dingtalk"
	"my-agent-go/internal/connectors/feishu"
	"my-agent-go/internal/connectors/mail"
	"my-agent-go/internal/connectors/notion"
	"my-agent-go/internal/connectors/slack"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

// defaultMailConnectorID 是首次启动播种的 mail 定义 ID。
const defaultMailConnectorID = "mail-default"

// defaultConnectorApprovalTTL 与 policy 的审批有效期对齐。
const defaultConnectorApprovalTTL = 60 * time.Second

// ConnectorListResult 是连接器列表。
type ConnectorListResult struct {
	Items []connectors.Status `json:"items"`
	Error *CallError          `json:"error,omitempty"`
}

// ConnectorResult 是单个连接器操作的结果。
type ConnectorResult struct {
	Item  connectors.Status `json:"item"`
	Error *CallError        `json:"error,omitempty"`
}

// ConnectorHealthResult 是一次健康检查结果。
type ConnectorHealthResult struct {
	Health connectors.Health `json:"health"`
	Error  *CallError        `json:"error,omitempty"`
}

// ConnectorSaveRequest 是保存连接器定义的请求（不含任何秘密）。
type ConnectorSaveRequest struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	// Kind 省略时按 mail 处理（C08a 只实现 mail；其它 kind 会被结构化拒绝）。
	Kind             string                `json:"kind,omitempty"`
	Endpoints        connectors.Endpoints  `json:"endpoints"`
	Enabled          bool                  `json:"enabled"`
	Capabilities     []string              `json:"capabilities"`
	AllowedResources []string              `json:"allowedResources"`
	RateLimit        *connectors.RateLimit `json:"rateLimit,omitempty"`
	ExpectedRevision uint64                `json:"expectedRevision"`
}

// connectorService 返回应用级连接器服务（懒加载单例）。
func (d *Desktop) connectorService() (*connectors.Service, error) {
	d.connectorMu.Lock()
	defer d.connectorMu.Unlock()
	if d.connectorSvc != nil {
		return d.connectorSvc, nil
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(d.root, "connectors.db"))
	if err != nil {
		return nil, err
	}
	// 迁移在这里做：连接器页可能在用户还没跑过任何 run 时就被打开，
	// 不能指望"有人先跑一次"把表建出来。
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store, err := connectors.NewSQLStore(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	creds, err := d.connectorCredentials()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	pol := policy.New(policy.Options{
		Permissions: effectivePermissions(d.Settings()),
		ApprovalTTL: defaultConnectorApprovalTTL,
	})
	svc, err := connectors.NewService(connectors.Options{
		Definitions: store,
		// 同一份 SQLStore 既是定义存储也是审计存储：审计与定义在同一个库里，
		// 备份/迁移时不会只带走一半。
		Audit:         store,
		Credentials:   creds,
		Policy:        pol,
		DriverFactory: connectorDriver,
		OnPending:     d.rememberConnectorApproval,
		ApprovalWait:  defaultConnectorApprovalTTL,
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := d.seedDefaultConnector(ctx, store); err != nil {
		_ = db.Close()
		return nil, err
	}
	d.connectorSvc = svc
	d.connectorDB = db
	return svc, nil
}

// connectorDriver 按定义装配适配器。
//
// 装配层只回答"哪个 kind 用哪个适配器"：契约、风险、限流、策略都不在这层，
// 否则每加一个平台就多一条可能的绕过路径。
func connectorDriver(def connectors.Definition) (connectors.Driver, error) {
	switch def.Kind {
	case connectors.KindMail:
		return mail.New(mail.Settings{Endpoints: def.Endpoints})
	case connectors.KindDingtalk:
		return dingtalk.New(dingtalk.Settings{Endpoints: def.Endpoints})
	case connectors.KindFeishu:
		return feishu.New(feishu.Settings{Endpoints: def.Endpoints})
	case connectors.KindSlack:
		return slack.New(slack.Settings{Endpoints: def.Endpoints})
	case connectors.KindNotion:
		return notion.New(notion.Settings{Endpoints: def.Endpoints})
	}
	return nil, contracts.NewAppError(
		contracts.CodeUnsupported, "没有该类型的连接器驱动: "+string(def.Kind), false)
}

// seedDefaultConnector 在首次打开时放一条"未启用、未配置"的 mail 定义。
//
// 不预置端点、不预置密码：预置一个看起来能用的连接器会让界面在用户什么都没
// 做的情况下显示一个坏掉的功能。
func (d *Desktop) seedDefaultConnector(ctx context.Context, store connectors.DefinitionStore) error {
	_, err := store.Get(ctx, defaultMailConnectorID)
	if err == nil {
		return nil
	}
	if !isConnectorNotFound(err) {
		return err
	}
	def := connectors.DefaultMailDefinition(defaultMailConnectorID, "connector:"+defaultMailConnectorID)
	if _, perr := store.Put(ctx, def); perr != nil {
		return perr
	}
	return nil
}

// connectorCredentials 复用模型凭证库（同一个 OS 保护存储）。
func (d *Desktop) connectorCredentials() (credentials.Store, error) {
	d.svcMu.Lock()
	defer d.svcMu.Unlock()
	if d.creds != nil {
		return d.creds, nil
	}
	store, err := credentials.NewNativeStore(filepath.Join(d.root, "credentials"))
	if err != nil {
		return nil, err
	}
	d.creds = store
	return store, nil
}

// ListConnectors 返回全部连接器（含真实健康检查）。
func (d *Desktop) ListConnectors() ConnectorListResult {
	out := ConnectorListResult{Items: []connectors.Status{}}
	svc, err := d.connectorService()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	items, err := svc.ListStatuses(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if items != nil {
		out.Items = items
	}
	return out
}

// SaveConnector 保存定义（CAS）。只接受 ref 与非敏感端点，不接收任何秘密。
func (d *Desktop) SaveConnector(req ConnectorSaveRequest) ConnectorResult {
	svc, err := d.connectorService()
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	kind := connectors.Kind(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = connectors.KindMail
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return ConnectorResult{Error: toCallError(contracts.NewAppError(
			contracts.CodeUnsupported, "连接器 ID 不能为空", false))}
	}
	def := connectors.Definition{
		ID:               id,
		Kind:             kind,
		DisplayName:      strings.TrimSpace(req.DisplayName),
		CredentialRef:    "connector:" + id,
		Enabled:          req.Enabled,
		AllowedResources: append([]string{}, req.AllowedResources...),
		Endpoints:        req.Endpoints,
		Revision:         req.ExpectedRevision,
	}
	for _, c := range req.Capabilities {
		def.Capabilities = append(def.Capabilities, connectors.Capability(strings.TrimSpace(c)))
	}
	if def.DisplayName == "" {
		def.DisplayName = string(kind)
	}
	if req.RateLimit != nil {
		def.RateLimit = *req.RateLimit
	}
	saved, err := svc.SaveDefinition(context.Background(), def)
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	status, err := svc.Status(context.Background(), saved.ID)
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	return ConnectorResult{Item: status}
}

// ConnectConnector 用用户提供的秘密完成授权。
//
// secret 只往凭证库写一次；返回值里没有它，日志/审计里也没有它。
func (d *Desktop) ConnectConnector(id, secret string) ConnectorResult {
	svc, err := d.connectorService()
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	status, err := svc.Connect(context.Background(), id, []byte(secret))
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	return ConnectorResult{Item: status}
}

// DisconnectConnector 撤销授权（删除凭证）。
func (d *Desktop) DisconnectConnector(id string) ConnectorResult {
	svc, err := d.connectorService()
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	status, err := svc.Disconnect(context.Background(), id)
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	return ConnectorResult{Item: status}
}

// ConnectorStatus 返回单条连接器状态。
func (d *Desktop) ConnectorStatus(id string) ConnectorResult {
	svc, err := d.connectorService()
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	status, err := svc.Status(context.Background(), id)
	if err != nil {
		return ConnectorResult{Error: toCallError(err)}
	}
	return ConnectorResult{Item: status}
}

// ConnectorHealth 执行一次真实健康检查。
func (d *Desktop) ConnectorHealth(id string) ConnectorHealthResult {
	svc, err := d.connectorService()
	if err != nil {
		return ConnectorHealthResult{Error: toCallError(err)}
	}
	health, err := svc.Health(context.Background(), id)
	if err != nil {
		return ConnectorHealthResult{Error: toCallError(err)}
	}
	return ConnectorHealthResult{Health: health}
}

// PendingConnectorApprovals 返回等待人工确认的连接器动作（不含凭证）。
func (d *Desktop) PendingConnectorApprovals() []connectors.PendingApproval {
	d.connectorMu.Lock()
	defer d.connectorMu.Unlock()
	out := make([]connectors.PendingApproval, 0, len(d.connectorPending))
	for _, pa := range d.connectorPending {
		out = append(out, pa)
	}
	return out
}

// ResolveConnectorApproval 答复一条连接器审批（once / session_scope / deny）。
//
// 说明：Pi 侧的通用策略扩展也会对这些工具问一次审批，C08b 会把它收敛成
// "handler 是唯一执行点"（delegate-to-handler）。当前实现保证的是：即使没有
// 那次委托，连接器 handler 自己仍然会先过 policy 才派发。
func (d *Desktop) ResolveConnectorApproval(approvalID, choice string) error {
	svc, err := d.connectorService()
	if err != nil {
		return err
	}
	if err := svc.ResolveApproval(context.Background(), approvalID, policy.ApprovalChoice(choice)); err != nil {
		return err
	}
	d.connectorMu.Lock()
	delete(d.connectorPending, approvalID)
	d.connectorMu.Unlock()
	return nil
}

// rememberConnectorApproval 记录待审批请求，供 UI 轮询。
func (d *Desktop) rememberConnectorApproval(pa connectors.PendingApproval) {
	d.connectorMu.Lock()
	defer d.connectorMu.Unlock()
	if d.connectorPending == nil {
		d.connectorPending = map[string]connectors.PendingApproval{}
	}
	d.connectorPending[pa.ApprovalID] = pa
}

func isConnectorNotFound(err error) bool {
	var appErr *contracts.AppError
	return errors.As(err, &appErr) && appErr != nil && appErr.Code == contracts.CodeNotFound
}
