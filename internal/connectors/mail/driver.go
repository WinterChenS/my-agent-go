// Package mail 是邮箱连接器（IMAP/SMTP）。
//
// 动作严格按 Anvil action catalog：search / read / attachment / send。
// C08a 的驱动范围就是这四个 + 健康探针；draft（IMAP APPEND）留给 C08b，
// 这里不提供空实现。
//
// 结果语义（CONN06）：
//   - send 是非幂等动作：终止点写出去之后的超时/断线是 OutcomeUnknown，
//     并且**不会**自动重放；
//   - 读动作失败是 OutcomeFailed，Dispatched=false，可以安全再试。
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/contracts"
)

// Settings 是驱动需要的非敏感连接参数。
type Settings struct {
	Endpoints connectors.Endpoints
	// Hostname 用于 EHLO 与 Message-ID 域名。
	Hostname string
}

// Driver 实现 connectors.Driver。
type Driver struct {
	settings Settings
	now      func() time.Time
}

// New 构造驱动。
//
// 端点/用户名/发件人缺失时直接失败：让"没配置"以结构化错误出现在连接器页，
// 而不是等到第一次发信才炸。
func New(settings Settings) (*Driver, error) {
	ep := settings.Endpoints
	if strings.TrimSpace(ep.IMAPAddr) == "" || strings.TrimSpace(ep.SMTPAddr) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "mail 连接器缺少 IMAP/SMTP 端点", false)
	}
	if strings.TrimSpace(ep.Username) == "" || strings.TrimSpace(ep.From) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "mail 连接器缺少用户名/发件人", false)
	}
	return &Driver{settings: settings, now: time.Now}, nil
}

// Kind 返回连接器类型。
func (d *Driver) Kind() connectors.Kind { return connectors.KindMail }

// Capabilities 返回本驱动真正实现的能力。
func (d *Driver) Capabilities() []connectors.Capability {
	return []connectors.Capability{connectors.CapabilityMailRead, connectors.CapabilityMailSend}
}

// Actions 返回 catalog 里的动作（风险/幂等由 catalog 决定）。
func (d *Driver) Actions(ctx context.Context) ([]connectors.ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connectors.Catalog(connectors.KindMail), nil
}

// Diagnose 做一次通过认证的健康检查：IMAP 登录并选中 INBOX，SMTP 连接并 AUTH。
//
// 只有两步都成功才是 connected——只连得上 LSMTP 并不说明能发信（认证可能失败）。
func (d *Driver) Diagnose(ctx context.Context, cred connectors.Credential) (connectors.Health, error) {
	password := string(cred.Secret)
	imap, err := dialIMAP(ctx, d.settings.Endpoints, password)
	if err != nil {
		return healthFromError(err, "IMAP"), nil
	}
	defer imap.close()
	if err := imap.selectInbox(); err != nil {
		return healthFromError(err, "IMAP"), nil
	}
	if err := probeSMTP(ctx, d.settings.Endpoints, password); err != nil {
		return healthFromError(err, "SMTP"), nil
	}
	return connectors.Health{
		Status:      connectors.HealthConnected,
		Detail:      "IMAP/SMTP 认证通过",
		Remediation: connectors.RemediationNone,
		CheckedAt:   d.now().UTC(),
	}, nil
}

// healthFromError 把认证/网络错误分类成 blocked（需要人处理）而不是 connected。
func healthFromError(err error, stage string) connectors.Health {
	status := connectors.HealthBlocked
	remediation := connectors.RemediationCheckNetwork
	detail := stage + " 健康检查失败: " + err.Error()
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr != nil {
		switch appErr.Code {
		case contracts.CodeUnauthorized:
			remediation = connectors.RemediationAddCredentials
			detail = stage + " 认证失败（凭证可能已失效）: " + appErr.Message
		case contracts.CodeProviderUnreachable:
			remediation = connectors.RemediationCheckNetwork
			status = connectors.HealthError
		case contracts.CodeProviderError:
			status = connectors.HealthError
		}
	}
	return connectors.Health{Status: status, Detail: detail, Remediation: remediation}
}

// Invoke 执行 catalog 动作。
func (d *Driver) Invoke(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	switch inv.ActionID {
	case "search":
		return d.search(ctx, inv)
	case "read":
		return d.read(ctx, inv)
	case "attachment":
		return d.attachment(ctx, inv)
	case "send":
		return d.send(ctx, inv)
	default:
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "mail 驱动不支持动作: "+inv.ActionID, false)
	}
}

func (d *Driver) search(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "search 参数不合法: "+err.Error(), false)
	}
	if strings.TrimSpace(args.Query) == "" {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "search 需要 query", false)
	}
	client, err := dialIMAP(ctx, d.settings.Endpoints, string(inv.Credential.Secret))
	if err != nil {
		return connectors.Result{}, err
	}
	defer client.close()
	if err := client.selectInbox(); err != nil {
		return connectors.Result{}, err
	}
	messages, err := client.listMessages(args.Query, args.Limit)
	if err != nil {
		return connectors.Result{}, err
	}
	data, err := marshalJSON(map[string]any{"messages": messages})
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("命中 %d 封邮件", len(messages)),
		Data:        data,
		CompletedAt: d.now().UTC(),
	}, nil
}

func (d *Driver) read(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		UID int `json:"uid"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "read 参数不合法: "+err.Error(), false)
	}
	if args.UID <= 0 {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "read 需要正整数 uid", false)
	}
	client, err := dialIMAP(ctx, d.settings.Endpoints, string(inv.Credential.Secret))
	if err != nil {
		return connectors.Result{}, err
	}
	defer client.close()
	if err := client.selectInbox(); err != nil {
		return connectors.Result{}, err
	}
	detail, err := client.readMessage(args.UID)
	if err != nil {
		return connectors.Result{}, err
	}
	// 读取动作只返回元数据与正文，不在不带名字的情况下把附件字节全塞进结果。
	for i := range detail.Attachments {
		detail.Attachments[i].DataB64 = ""
	}
	data, err := marshalJSON(detail)
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      "已读取邮件: " + detail.Subject,
		Data:        data,
		CompletedAt: d.now().UTC(),
	}, nil
}

func (d *Driver) attachment(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		UID  int    `json:"uid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "attachment 参数不合法: "+err.Error(), false)
	}
	if args.UID <= 0 || strings.TrimSpace(args.Name) == "" {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "attachment 需要 uid 与 name", false)
	}
	client, err := dialIMAP(ctx, d.settings.Endpoints, string(inv.Credential.Secret))
	if err != nil {
		return connectors.Result{}, err
	}
	defer client.close()
	if err := client.selectInbox(); err != nil {
		return connectors.Result{}, err
	}
	detail, err := client.readMessage(args.UID)
	if err != nil {
		return connectors.Result{}, err
	}
	for _, att := range detail.Attachments {
		if att.Name != args.Name {
			continue
		}
		data, err := marshalJSON(att)
		if err != nil {
			return connectors.Result{}, err
		}
		return connectors.Result{
			Outcome:     connectors.OutcomeDone,
			Detail:      fmt.Sprintf("已取回附件 %s（%d bytes）", att.Name, att.Size),
			Data:        data,
			CompletedAt: d.now().UTC(),
		}, nil
	}
	return connectors.Result{}, contracts.NewAppError(
		contracts.CodeNotFound, "邮件里没有名为 "+args.Name+" 的附件", false)
}

func (d *Driver) send(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "send 参数不合法: "+err.Error(), false)
	}
	if len(args.To) == 0 {
		return connectors.Result{}, contracts.NewAppError(contracts.CodeUnsupported, "send 至少需要一个收件人", false)
	}
	dispatched, err := sendViaSMTP(ctx, d.settings.Endpoints, string(inv.Credential.Secret), outgoing{
		To:             args.To,
		Subject:        args.Subject,
		Body:           args.Body,
		IdempotencyKey: inv.IdempotencyKey,
	})
	if err != nil {
		if dispatched {
			// 终止点已写出：服务器可能已经收下这封信。这是结果未知，不是失败。
			return connectors.Result{
				Outcome:    connectors.OutcomeUnknown,
				Dispatched: true,
				Detail:     "邮件终止点已发出，但未收到服务器确认: " + err.Error(),
			}, nil
		}
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Dispatched:  true,
		Detail:      fmt.Sprintf("已发送给 %d 个收件人", len(args.To)),
		CompletedAt: d.now().UTC(),
	}, nil
}
