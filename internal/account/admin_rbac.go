package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 权限键，与后台权限矩阵的每一行一一对应。/api/ 下的 GET 对所有角色开放，只有云端监控（view_cloud_usage）和服务日志（view_logs）的读取受权限限制；其余权限控制写操作。
const (
	PermReviewDictPR      = "review_dict_pr"
	PermReviewCommunity   = "review_community" // also covers the sensitive word list
	PermTriageIssues      = "triage_issues"    // also covers creating a GitHub issue from a crash group
	PermBanUsers          = "ban_users"
	PermPublishNotices    = "publish_notices"
	PermTriggerRelease    = "trigger_release"
	PermViewCloudUsage    = "view_cloud_usage"
	PermViewLogs          = "view_logs" // 读取服务日志（GET /api/logs、/api/logs/stream），默认只有维护者拥有
	PermManagePermissions = "manage_permissions"
)

// RoleMaintainer is the built-in role owners always hold and existing members were migrated to.
const RoleMaintainer = "maintainer"

// AllAdminPermissions returns every permission key in matrix order.
func AllAdminPermissions() []string {
	return []string{PermReviewDictPR, PermReviewCommunity, PermTriageIssues, PermBanUsers, PermPublishNotices, PermTriggerRelease, PermViewCloudUsage, PermViewLogs, PermManagePermissions}
}

// AdminAccess is who is calling an admin endpoint and what they may do. The server builds it once per request, after authentication, and every handler reads it from the context.
type AdminAccess struct {
	// Actor is the audit identity: "google:<sub>:<email>", "pat:<email>" or "legacy-token".
	Actor string `json:"-"`
	// Email is empty for the legacy token.
	Email string `json:"email"`
	Role  string `json:"role"`
	// Permissions is sorted in matrix order.
	Permissions []string `json:"permissions"`
	// Owner is set for the deployment-configured owners only.
	Owner bool `json:"owner"`
}

type adminAccessKey struct{}

// WithAdminAccess stores access, and its actor, in the context.
func WithAdminAccess(ctx context.Context, access AdminAccess) context.Context {
	return context.WithValue(WithAdminActor(ctx, access.Actor), adminAccessKey{}, access)
}

// AdminAccessFrom returns the access stored by WithAdminAccess.
func AdminAccessFrom(ctx context.Context) (AdminAccess, bool) {
	access, ok := ctx.Value(adminAccessKey{}).(AdminAccess)
	return access, ok
}

// AdminCan reports whether the caller holds perm. A context without AdminAccess holds nothing.
func AdminCan(ctx context.Context, perm string) bool {
	access, ok := AdminAccessFrom(ctx)
	return ok && slices.Contains(access.Permissions, perm)
}

// errPermissionDenied is returned by an action whose caller lacks a permission it checks itself; the action dispatcher answers 403 permission_denied.
var errPermissionDenied = errors.New("permission_denied")

// errNotImplemented is what console stubs return until their unit implements them; handlers answer 501 not_implemented.
var errNotImplemented = errors.New("not_implemented")

// requirePerm writes 403 permission_denied and returns false when the caller lacks perm.
func requirePerm(w http.ResponseWriter, r *http.Request, perm string) bool {
	if AdminCan(r.Context(), perm) {
		return true
	}
	writeError(w, 403, "permission_denied")
	return false
}

// checkPerm is requirePerm for actions: it returns errPermissionDenied instead of writing.
func checkPerm(ctx context.Context, perm string) error {
	if AdminCan(ctx, perm) {
		return nil
	}
	return errPermissionDenied
}

// notImplemented answers a console endpoint whose unit has not been implemented.
func notImplemented(w http.ResponseWriter) { writeError(w, 501, "not_implemented") }

// AdminMemberRole resolves an enabled admin_members row to its role and that role's permissions in matrix order. A missing or disabled member is ErrInvalid. Owners and the legacy token are resolved by the server without this lookup.
func (a *Service) AdminMemberRole(ctx context.Context, email string) (string, []string, error) {
	var role string
	var granted []string
	err := a.store.pool.QueryRow(ctx, `SELECT m.role,COALESCE(array_agg(p.permission) FILTER (WHERE p.permission IS NOT NULL),'{}') FROM admin_members m LEFT JOIN admin_role_permissions p ON p.role=m.role WHERE m.email=$1 AND m.enabled GROUP BY m.role`, strings.ToLower(email)).Scan(&role, &granted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrInvalid
	}
	if err != nil {
		return "", nil, err
	}
	ordered := []string{}
	for _, perm := range AllAdminPermissions() {
		if slices.Contains(granted, perm) {
			ordered = append(ordered, perm)
		}
	}
	return role, ordered, nil
}

// auditTx records a successful admin write inside tx, so the record commits or rolls back with the write it describes. detail is any JSON object (a struct or map) with what the console needs to phrase the entry, such as a reason, a count or the old and new values; nil records {}.
func auditTx(ctx context.Context, tx pgx.Tx, action, target string, detail any) error {
	raw, err := auditDetail(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_audit(action,target,actor,detail) VALUES($1,$2,$3,$4)`, action, target, adminActor(ctx), raw)
	return err
}

// Audit records an admin write whose effect lives outside the database, such as a GitHub merge or a Telegram message. Call it only after the external call succeeded; a failed call is not audited.
func (a *Service) Audit(ctx context.Context, action, target string, detail any) error {
	raw, err := auditDetail(detail)
	if err != nil {
		return err
	}
	_, err = a.store.pool.Exec(ctx, `INSERT INTO admin_audit(action,target,actor,detail) VALUES($1,$2,$3,$4)`, action, target, adminActor(ctx), raw)
	return err
}

func auditDetail(detail any) (string, error) {
	if detail == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || raw[0] != '{' {
		return "", errors.New("audit detail must be a JSON object")
	}
	return string(raw), nil
}

// AdminService is one upstream service the console monitors, from admin.services in the deployment configuration.
type AdminService struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// QuotaLimit and QuotaUnit ("calls", "chars", "hours" or "cny") describe the monthly quota; a zero limit means none is configured.
	QuotaLimit float64 `json:"quota_limit"`
	QuotaUnit  string  `json:"quota_unit"`
	// UnitPrice is the estimated CNY cost of one metered unit (one call, character or hour, as the service meters usage); zero means no estimate.
	UnitPrice float64 `json:"unit_price"`
	// SlowMS is the P95 latency above which the service counts as degraded.
	SlowMS int `json:"slow_ms"`
}

// AdminSettings is the part of the admin deployment configuration the account side needs.
type AdminSettings struct {
	// Owners are the configured owner emails, lowercase.
	Owners []string
	// Environment is the label the console shows next to the version, for example "生产环境".
	Environment string
	Services    []AdminService
	// DerivedServices are the upstreams the status probe monitors when Services is empty, under their default names; the overview lists them so a deployment without admin.services still sees named services.
	DerivedServices []AdminService
}

// ConfigureAdmin is called once during server construction, before serving requests, when the admin host is enabled.
func (a *Service) ConfigureAdmin(c AdminSettings) {
	if a != nil {
		a.admin = c
	}
}

// isAdminOwner reports whether email is one of the configured owners.
func (a *Service) isAdminOwner(email string) bool {
	for _, owner := range a.admin.Owners {
		if strings.EqualFold(owner, email) {
			return true
		}
	}
	return false
}
