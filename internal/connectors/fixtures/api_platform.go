package fixtures

// 五个平台的路由形状。
//
// 形状参照各平台公开 REST 文档（钉钉 v1.0、飞书 open-apis、Slack Web API、
// Notion v1、GitHub REST v3），但只实现 V1 动作需要的那几个端点：
// 读动作有稳定游标分页，写动作有独立计数器，认证失败的形状与平台一致
// （Slack/飞书在 HTTP 200 里用 ok:false / code!=0 表达拒绝）。

import (
	"net/http"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 钉钉
// ---------------------------------------------------------------------------

func (a *API) serveDingtalk(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/oauth2/accessToken":
		var in struct {
			AppKey    string `json:"appKey"`
			AppSecret string `json:"appSecret"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if !a.checkApp(in.AppKey, in.AppSecret) {
			a.counter.Update(func(sn *Snapshot) { sn.HTTPUnauthorized++ })
			writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "InvalidAuthentication", "message": "appKey/appSecret 不匹配"})
			return false
		}
		token := a.issueToken(PlatformDingtalk)
		a.counter.Update(func(sn *Snapshot) { sn.DingtalkTokensIssued++ })
		writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "expireIn": 7200})
		return false

	case r.Method == http.MethodGet && r.URL.Path == "/v1.0/contact/scopes":
		if !a.requireBearer(w, r) {
			return false
		}
		items, next, more := paginate(dingtalkScopes(), r.URL.Query().Get("nextToken"), a.opts.pageSize(), "dt-cursor-")
		body := map[string]any{"scopes": items}
		if more {
			body["nextToken"] = next
		}
		writeJSON(w, http.StatusOK, body)
		return false

	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/robot/groupMessages/send":
		if !a.requireBearer(w, r) {
			return false
		}
		var in struct {
			OpenConversationID string `json:"openConversationId"`
			MsgKey             string `json:"msgKey"`
			MsgParam           string `json:"msgParam"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if strings.TrimSpace(in.OpenConversationID) == "" {
			badRequest(w, "缺少 openConversationId")
			return false
		}
		if a.apply("send_group", in.OpenConversationID, in.MsgParam, func(sn *Snapshot) { sn.DingtalkGroupMessages++ }) {
			writeJSON(w, http.StatusOK, map[string]any{"processQueryKey": "pq-fixture-" + in.OpenConversationID})
		}
		return true
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"code": "NotFound", "message": r.URL.Path})
	return false
}

func dingtalkScopes() []map[string]any {
	return []map[string]any{
		{"scopeId": "scope-1", "name": "contact.department.read"},
		{"scopeId": "scope-2", "name": "contact.user.read"},
		{"scopeId": "scope-3", "name": "chat.message.send"},
		{"scopeId": "scope-4", "name": "calendar.event.read"},
		{"scopeId": "scope-5", "name": "drive.file.read"},
	}
}

// ---------------------------------------------------------------------------
// 飞书
// ---------------------------------------------------------------------------

func (a *API) serveFeishu(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
		var in struct {
			AppID     string `json:"app_id"`
			AppSecret string `json:"app_secret"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if !a.checkApp(in.AppID, in.AppSecret) {
			a.counter.Update(func(sn *Snapshot) { sn.HTTPUnauthorized++ })
			writeJSON(w, http.StatusOK, map[string]any{"code": 99991663, "msg": "app_id 或 app_secret 不正确"})
			return false
		}
		token := a.issueToken(PlatformFeishu)
		a.counter.Update(func(sn *Snapshot) { sn.FeishuTokensIssued++ })
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 0, "msg": "ok", "tenant_access_token": token, "expire": 7200,
		})
		return false

	case r.Method == http.MethodGet && isFeishuBlocksPath(r.URL.Path):
		if !a.requireBearer(w, r) {
			return false
		}
		docID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/open-apis/docx/v1/documents/"), "/blocks")
		a.mu.Lock()
		content, ok := a.docs[docID]
		a.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": 1770001, "msg": "文档不存在"})
			return false
		}
		items, next, more := paginate(feishuBlocks(content), r.URL.Query().Get("page_token"), a.opts.pageSize(), "fs-page-")
		data := map[string]any{"items": items, "has_more": more}
		if more {
			data["page_token"] = next
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "data": data})
		return false

	case r.Method == http.MethodPost && isFeishuBatchUpdatePath(r.URL.Path):
		if !a.requireBearer(w, r) {
			return false
		}
		docID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/open-apis/docx/v1/documents/"), "/blocks/batch_update")
		var in struct {
			Requests []map[string]any `json:"requests"`
			Content  string           `json:"content"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		a.mu.Lock()
		if _, ok := a.docs[docID]; !ok {
			a.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]any{"code": 1770001, "msg": "文档不存在"})
			return false
		}
		a.docs[docID] = in.Content
		a.docRev++
		rev := a.docRev
		a.mu.Unlock()
		if a.apply("update_doc", docID, in.Content, func(sn *Snapshot) { sn.FeishuDocUpdated++ }) {
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0, "msg": "ok", "data": map[string]any{"document_revision_id": rev},
			})
		}
		return true

	case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
		if !a.requireBearer(w, r) {
			return false
		}
		var in struct {
			ReceiveID string `json:"receive_id"`
			MsgType   string `json:"msg_type"`
			Content   string `json:"content"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if strings.TrimSpace(in.ReceiveID) == "" {
			badRequest(w, "缺少 receive_id")
			return false
		}
		if a.apply("send_message", in.ReceiveID, in.Content, func(sn *Snapshot) { sn.FeishuMessages++ }) {
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om-fixture-" + in.ReceiveID},
			})
		}
		return true
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"code": 404, "msg": r.URL.Path})
	return false
}

func isFeishuBlocksPath(path string) bool {
	return strings.HasPrefix(path, "/open-apis/docx/v1/documents/") && strings.HasSuffix(path, "/blocks")
}

func isFeishuBatchUpdatePath(path string) bool {
	return strings.HasPrefix(path, "/open-apis/docx/v1/documents/") && strings.HasSuffix(path, "/blocks/batch_update")
}

func feishuBlocks(content string) []map[string]any {
	out := []map[string]any{}
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, map[string]any{
			"block_id":   "blk-" + strconv.Itoa(i+1),
			"block_type": 2,
			"text":       line,
		})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"block_id": "blk-empty", "block_type": 2, "text": ""})
	}
	return out
}

// ---------------------------------------------------------------------------
// Slack
// ---------------------------------------------------------------------------

// requireSlackAuth 按 Slack 的真实形状拒绝坏令牌：HTTP 200 + ok:false。
func (a *API) requireSlackAuth(w http.ResponseWriter, r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	got := ""
	if strings.HasPrefix(header, "Bearer ") {
		got = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	a.mu.Lock()
	ok := got != "" && a.tokens[got]
	a.mu.Unlock()
	if ok {
		return true
	}
	a.counter.Update(func(sn *Snapshot) { sn.HTTPUnauthorized++ })
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"})
	return false
}

func (a *API) serveSlack(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth.test":
		if !a.requireSlackAuth(w, r) {
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "team": "fixture-workspace", "user_id": "U-FIXTURE", "bot_id": "B-FIXTURE",
		})
		return false

	case r.Method == http.MethodGet && r.URL.Path == "/api/conversations.history":
		if !a.requireSlackAuth(w, r) {
			return false
		}
		channel := r.URL.Query().Get("channel")
		if strings.TrimSpace(channel) == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "channel_not_found"})
			return false
		}
		items, next, more := paginate(slackMessages(), r.URL.Query().Get("cursor"), a.opts.pageSize(), "sl-cursor-")
		body := map[string]any{"ok": true, "messages": items, "has_more": more}
		if more {
			body["response_metadata"] = map[string]any{"next_cursor": next}
		}
		writeJSON(w, http.StatusOK, body)
		return false

	case r.Method == http.MethodPost && r.URL.Path == "/api/chat.postMessage":
		if !a.requireSlackAuth(w, r) {
			return false
		}
		var in struct {
			Channel  string `json:"channel"`
			ThreadTS string `json:"thread_ts"`
			Text     string `json:"text"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if strings.TrimSpace(in.Channel) == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "channel_not_found"})
			return false
		}
		if a.apply("reply_thread", in.Channel, in.Text, func(sn *Snapshot) { sn.SlackReplies++ }) {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "channel": in.Channel, "ts": "1700000001.000100", "thread_ts": in.ThreadTS,
			})
		}
		return true
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "unknown_method"})
	return false
}

func slackMessages() []map[string]any {
	out := []map[string]any{}
	for i := 1; i <= 5; i++ {
		out = append(out, map[string]any{
			"type":        "message",
			"ts":          "170000000" + strconv.Itoa(i) + ".00000" + strconv.Itoa(i),
			"user":        "U-FIXTURE",
			"text":        "消息 " + strconv.Itoa(i),
			"thread_ts":   "170000000" + strconv.Itoa(i) + ".00000" + strconv.Itoa(i),
			"reply_count": 0,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Notion
// ---------------------------------------------------------------------------

func (a *API) serveNotion(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/users/me":
		if !a.requireBearer(w, r) {
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "user", "id": "u-fixture", "name": "Fixture Bot", "type": "bot",
		})
		return false

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/databases/") && strings.HasSuffix(r.URL.Path, "/query"):
		if !a.requireBearer(w, r) {
			return false
		}
		var in struct {
			PageSize    int    `json:"page_size"`
			StartCursor string `json:"start_cursor"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		items, next, more := paginate(notionPages(), in.StartCursor, a.opts.pageSize(), "nt-cursor-")
		body := map[string]any{"object": "list", "results": items, "has_more": more}
		if more {
			body["next_cursor"] = next
		}
		writeJSON(w, http.StatusOK, body)
		return false

	case r.Method == http.MethodPost && r.URL.Path == "/v1/pages":
		if !a.requireBearer(w, r) {
			return false
		}
		var in struct {
			Parent     map[string]string `json:"parent"`
			Properties map[string]any    `json:"properties"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		parent := strings.TrimSpace(in.Parent["page_id"])
		if parent == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"object": "error", "code": "validation_error", "message": "parent.page_id 缺失",
			})
			return false
		}
		title := notionTitle(in.Properties)
		a.mu.Lock()
		a.pages["page-new"] = title
		a.mu.Unlock()
		if a.apply("create_page", parent, title, func(sn *Snapshot) { sn.NotionPagesCreated++ }) {
			writeJSON(w, http.StatusOK, map[string]any{
				"object": "page", "id": "page-new", "title": title,
			})
		}
		return true

	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/pages/"):
		if !a.requireBearer(w, r) {
			return false
		}
		pageID := strings.TrimPrefix(r.URL.Path, "/v1/pages/")
		var in struct {
			Properties map[string]any `json:"properties"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		a.mu.Lock()
		if _, ok := a.pages[pageID]; !ok {
			a.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]any{
				"object": "error", "code": "object_not_found", "message": "页面不存在",
			})
			return false
		}
		title := notionTitle(in.Properties)
		a.pages[pageID] = title
		a.mu.Unlock()
		if a.apply("update_page", pageID, title, func(sn *Snapshot) { sn.NotionPagesUpdated++ }) {
			writeJSON(w, http.StatusOK, map[string]any{
				"object": "page", "id": pageID, "title": title,
			})
		}
		return true
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"object": "error", "code": "not_found", "message": r.URL.Path})
	return false
}

// notionTitle 从 properties 里取标题（V1 只需要一个 title 属性）。
func notionTitle(props map[string]any) string {
	raw, ok := props["title"]
	if !ok {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	if m, ok := raw.(map[string]any); ok {
		if s, ok := m["title"].(string); ok {
			return s
		}
	}
	return ""
}

func notionPages() []map[string]any {
	out := []map[string]any{}
	for i := 1; i <= 5; i++ {
		id := "page-" + strconv.Itoa(i)
		out = append(out, map[string]any{
			"object":           "page",
			"id":               id,
			"title":            "页面 " + strconv.Itoa(i),
			"last_edited_time": "2026-11-0" + strconv.Itoa(i) + "T09:00:00Z",
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// GitHub
// ---------------------------------------------------------------------------

func (a *API) serveGitHub(w http.ResponseWriter, r *http.Request) bool {
	parts := splitPath(r.URL.Path)

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		if !a.requireBearer(w, r) {
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{"login": "fixture-bot", "id": 4242, "type": "User"})
		return false

	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "repos":
		if !a.requireBearer(w, r) {
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": 1, "full_name": parts[1] + "/" + parts[2], "default_branch": "main", "private": true,
		})
		return false

	case r.Method == http.MethodGet && len(parts) == 5 && parts[0] == "repos" && parts[3] == "issues":
		if !a.requireBearer(w, r) {
			return false
		}
		num, err := strconv.Atoi(parts[4])
		if err != nil {
			badRequest(w, "议题编号不是整数")
			return false
		}
		a.mu.Lock()
		title, ok := a.issues[num]
		a.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Issue Not Found"})
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"number": num, "title": title, "state": "open",
			"user": map[string]any{"login": "fixture-author"}, "html_url": "https://example.invalid/issues/" + strconv.Itoa(num),
		})
		return false

	case r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "repos" && parts[3] == "issues":
		if !a.requireBearer(w, r) {
			return false
		}
		repo := parts[1] + "/" + parts[2]
		var in struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if strings.TrimSpace(in.Title) == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "title 不能为空"})
			return false
		}
		a.mu.Lock()
		a.issues[4242] = in.Title
		a.mu.Unlock()
		if a.apply("create_issue", repo, in.Title, func(sn *Snapshot) { sn.GitHubIssuesCreated++ }) {
			writeJSON(w, http.StatusCreated, map[string]any{
				"number": 4242, "title": in.Title, "state": "open",
				"user": map[string]any{"login": "fixture-bot"},
			})
		}
		return true

	case r.Method == http.MethodGet && len(parts) == 5 && parts[0] == "repos" && parts[3] == "pulls":
		if !a.requireBearer(w, r) {
			return false
		}
		num, err := strconv.Atoi(parts[4])
		if err != nil {
			badRequest(w, "PR 编号不是整数")
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"number": num, "title": "连接器：五个适配器", "state": "open",
			"head": map[string]any{"ref": "feat/v1-foundation"}, "base": map[string]any{"ref": "main"},
		})
		return false

	case r.Method == http.MethodGet && len(parts) == 6 && parts[0] == "repos" && parts[3] == "pulls" && parts[5] == "files":
		if !a.requireBearer(w, r) {
			return false
		}
		records := githubFiles()
		cursor := r.URL.Query().Get("page")
		items, next, more := paginate(records, cursor, a.opts.pageSize(), "")
		if more {
			repo := parts[1] + "/" + parts[2]
			w.Header().Set("Link", "<"+a.BaseURL()+"/repos/"+repo+"/pulls/"+parts[4]+
				"/files?page="+next+"&per_page="+strconv.Itoa(a.opts.pageSize())+`>; rel="next"`)
		}
		writeJSON(w, http.StatusOK, items)
		return false

	case r.Method == http.MethodPost && len(parts) == 6 && parts[0] == "repos" && parts[3] == "issues" && parts[5] == "comments":
		if !a.requireBearer(w, r) {
			return false
		}
		repo := parts[1] + "/" + parts[2]
		var in struct {
			Body string `json:"body"`
		}
		if err := decodeBody(r, &in); err != nil {
			badRequest(w, "请求体不是合法 JSON")
			return false
		}
		if strings.TrimSpace(in.Body) == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "body 不能为空"})
			return false
		}
		num, err := strconv.Atoi(parts[4])
		if err != nil {
			badRequest(w, "议题编号不是整数")
			return false
		}
		a.mu.Lock()
		_, exists := a.issues[num]
		a.mu.Unlock()
		if !exists {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Issue Not Found"})
			return false
		}
		if a.apply("comment_pr", repo+"#"+strconv.Itoa(num), in.Body, func(sn *Snapshot) { sn.GitHubComments++ }) {
			writeJSON(w, http.StatusCreated, map[string]any{"id": 9001, "body": in.Body})
		}
		return true
	}

	writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	return false
}

func splitPath(path string) []string {
	out := []string{}
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func githubFiles() []map[string]any {
	out := []map[string]any{}
	for i := 1; i <= 5; i++ {
		out = append(out, map[string]any{
			"filename":  "file-" + strconv.Itoa(i) + ".go",
			"status":    "modified",
			"additions": i,
			"deletions": 0,
		})
	}
	return out
}
