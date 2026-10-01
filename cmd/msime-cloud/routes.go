package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/metasequoiaime/MSIME-Backend/internal/server"
)

// operation is one method on one path: from the published OpenAPI document for /v1, or from adminRoutes for /api.
type operation struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Auth    string `json:"auth"`
	Summary string `json:"summary"`
	// The OpenAPI operation object, or the admin route's notes.
	Detail json.RawMessage `json:"detail,omitempty"`
}

const (
	authNone    = "none"
	authUser    = "user"
	authAny     = "device-or-user"
	authAdmin   = "admin"
	adminPrefix = "/api/"
)

// The admin site's routes. They are dispatched by path inside the admin handler rather than registered on the mux, so the OpenAPI document does not describe them; admin_routes_test.go checks this table against that handler.
var adminRoutes = []struct {
	method, path, summary, notes string
}{
	{"GET", "/api/system", "服务版本与已配置的功能", ""},
	{"GET", "/api/overview", "用户、下载、崩溃与社区内容的概览统计", "query: days=7|30（默认 30）"},
	{"GET", "/api/users", "用户列表", "query: page（每页 50）、q（全文搜索）"},
	{"GET", "/api/users/{id}", "用户详情：登录方式、会话与发布数量", ""},
	{"GET", "/api/skins", "社区键盘皮肤列表", "query: page、q"},
	{"GET", "/api/skins/{id}", "社区键盘皮肤详情", ""},
	{"GET", "/api/candidate-skins", "社区候选窗皮肤列表，含公开与私有", "query: page、q、visibility=public|private"},
	{"GET", "/api/candidate-skins/{id}", "候选窗皮肤详情", ""},
	{"GET", "/api/plugins", "社区插件包列表", "query: page、q"},
	{"GET", "/api/plugins/{id}", "插件包详情", ""},
	{"GET", "/api/dictionaries", "社区词库列表", "query: page、q"},
	{"GET", "/api/dictionaries/{id}", "社区词库详情", ""},
	{"GET", "/api/replies", "社区回复模板列表", "query: page、q"},
	{"GET", "/api/replies/{id}", "回复模板详情", ""},
	{"GET", "/api/downloads", "下载记录", "query: page、q、platform、version"},
	{"GET", "/api/crashes", "崩溃报告", "query: page、q、platform、version、status=open|resolved"},
	{"GET", "/api/audit", "管理操作审计", "query: page、q、action、actor"},
	{"POST", "/api/actions", "执行一项管理操作（删除内容、撤销会话、处理崩溃），写入审计", `body: {"action": "revoke_session"|"revoke_sessions"|"delete_skin"|"delete_candidate_skin"|"delete_plugin"|"delete_dictionary"|"delete_reply"|"resolve_crash"|"reopen_crash", "id": "目标 ID（revoke_sessions 为用户 ID）", "user_id": "revoke_session 时会话所属用户"}。删除不可恢复。`},
	{"GET", "/api/site-settings", "官网设置：蓝奏云下载镜像链接", ""},
	{"POST", "/api/site-settings", "修改官网蓝奏云下载镜像链接", `body: {"lanzou_url": "https://... 或空字符串表示关闭"}`},
	{"GET", "/api/admins", "管理员成员（仅部署白名单中的超级管理员）", "管理员密钥没有邮箱，调用返回 403 owner_required。"},
	{"POST", "/api/admins", "添加、启用、停用或撤销管理员（仅超级管理员）", `body: {"email": "...", "action": "add"|"enable"|"disable"|"revoke"}`},
}

// catalog is every operation the CLI knows, /v1 from the OpenAPI document and /api from adminRoutes.
func catalog() ([]operation, error) {
	var spec struct {
		Security json.RawMessage                       `json:"security"`
		Paths    map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(server.OpenAPI(), &spec); err != nil {
		return nil, fmt.Errorf("the embedded API description is unreadable: %w", err)
	}
	var operations []operation
	for path, methods := range spec.Paths {
		for method, raw := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			var detail struct {
				Summary  string          `json:"summary"`
				Security json.RawMessage `json:"security"`
			}
			if err := json.Unmarshal(raw, &detail); err != nil {
				return nil, fmt.Errorf("the API description of %s %s is unreadable: %w", method, path, err)
			}
			security := detail.Security
			if security == nil {
				security = spec.Security
			}
			operations = append(operations, operation{Method: strings.ToUpper(method), Path: path, Auth: authOf(security), Summary: detail.Summary, Detail: raw})
		}
	}
	for _, route := range adminRoutes {
		notes, _ := json.Marshal(map[string]string{"summary": route.summary, "notes": route.notes})
		operations = append(operations, operation{Method: route.method, Path: route.path, Auth: authAdmin, Summary: route.summary, Detail: notes})
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Path != operations[j].Path {
			return operations[i].Path < operations[j].Path
		}
		return operations[i].Method < operations[j].Method
	})
	return operations, nil
}

// authOf names an OpenAPI security requirement: an empty list is anonymous, deviceToken alongside userSession accepts either.
func authOf(security json.RawMessage) string {
	var requirements []map[string]json.RawMessage
	_ = json.Unmarshal(security, &requirements)
	if len(requirements) == 0 {
		return authNone
	}
	for _, requirement := range requirements {
		if _, ok := requirement["deviceToken"]; ok {
			return authAny
		}
	}
	return authUser
}

// find returns the operation a request for method and path reaches, path being a template or a concrete path. A literal segment beats a parameter, the way the server's mux chooses /v1/skins/source over /v1/skins/{id}.
func find(operations []operation, method, path string) (operation, bool) {
	path, _, _ = strings.Cut(path, "?")
	best, bestScore := operation{}, -1
	for _, candidate := range operations {
		if candidate.Method != method {
			continue
		}
		if score, ok := match(candidate.Path, path); ok && score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best, bestScore >= 0
}

// match reports whether path fits template, scoring by the number of literal segments that agreed.
func match(template, path string) (int, bool) {
	want, have := strings.Split(strings.Trim(template, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
	score := 0
	for i, segment := range want {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "...}") {
			return score, len(have) > i
		}
		if i >= len(have) {
			return 0, false
		}
		if segment == have[i] {
			score++
			continue
		}
		if !strings.HasPrefix(segment, "{") || have[i] == "" {
			return 0, false
		}
	}
	return score, len(want) == len(have)
}

func listRoutes(out io.Writer, filter string) error {
	operations, err := catalog()
	if err != nil {
		return err
	}
	filter = strings.ToLower(filter)
	for _, op := range operations {
		line := fmt.Sprintf("%-6s %-52s %-14s %s", op.Method, op.Path, op.Auth, op.Summary)
		if filter == "" || strings.Contains(strings.ToLower(line), filter) {
			fmt.Fprintln(out, strings.TrimRight(line, " "))
		}
	}
	return nil
}

func describe(out io.Writer, method, path string) error {
	operations, err := catalog()
	if err != nil {
		return err
	}
	op, ok := find(operations, strings.ToUpper(method), path)
	if !ok {
		return usageError{fmt.Sprintf("no operation %s %s; `msime-cloud routes` lists them", strings.ToUpper(method), path)}
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(op)
}
