package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

//go:embed userdata_schema.sql
var userDataSchema string

//go:embed community_schema.sql
var communitySchema string

//go:embed admin_schema.sql
var adminSchema string

//go:embed translation_schema.sql
var translationSchema string

//go:embed candidate_skin_schema.sql
var candidateSkinSchema string

//go:embed community_candidate_skin_schema.sql
var communityCandidateSkinSchema string

//go:embed community_plugin_schema.sql
var communityPluginSchema string

//go:embed admin_ops_schema.sql
var adminOpsSchema string
var ErrInvalid = errors.New("invalid_credentials")
var ErrLimited = errors.New("rate_limit_exceeded")
var ErrConflict = errors.New("identity_already_linked")

type Store struct{ pool *pgxpool.Pool }
type User struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}
type Identity struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
}
type Challenge struct {
	IDHash, Provider, Subject, Nonce, CodeHash, LinkUser string
	// Set only for the Google server-exchange flow: the PKCE verifier and the loopback redirect URI the code was issued for.
	CodeVerifier, RedirectURI string
	Attempts                  int
}

// providerProfile holds the Google ID token claims mirrored onto auth_identities.
type providerProfile struct {
	Email         string
	EmailVerified bool
	Name, Picture string
}

// providerGrant is what a provider login stores next to the identity. SealedRefresh is already encrypted; empty means keep whatever is stored.
type providerGrant struct {
	Profile       *providerProfile
	SealedRefresh []byte
	Scope         string
}
type Principal struct {
	UserID, SessionID string
	CreatedAt         time.Time
}
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	User         User   `json:"user"`
}

func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, errors.New("用户数据库配置无效")
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	// 刚启动的 Pod 可能在网络和 DNS 就绪前就先连库,第一次失败不代表库不可用。一直重试到调用方的
	// 期限:之前第一次失败就退出进程,只能靠容器重启再来一次。最终的错误带上原因 —— pgx 的连接错误
	// 只有用户名、库名和地址,不含密码 —— 否则像证书过期这样的问题只剩一句「连接失败」。
	wait := 250 * time.Millisecond
	for {
		if e = p.Ping(ctx); e == nil {
			return &Store{p}, nil
		}
		select {
		case <-ctx.Done():
			p.Close()
			return nil, fmt.Errorf("用户数据库连接失败：%w", e)
		case <-time.After(wait):
		}
		slog.Warn("用户数据库暂时连不上，重试", "error", e)
		wait = min(wait*2, 2*time.Second)
	}
}
func (s *Store) Close()                            { s.pool.Close() }
func (s *Store) Migrate(ctx context.Context) error { return s.MigrateAs(ctx, "") }

// role 非空时在事务内切换到该角色再建表。切换只活在这个事务里,提交或回滚后连接回到原来的身份,
// 所以 DDL 权限不会留在连接池上。建出来的对象属于该角色,库里为它配的 default privileges 因此照常
// 生效 —— 新表自动把读写权限授予运行角色,不需要额外的 GRANT。
func (s *Store) MigrateAs(ctx context.Context, role string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(8372419)"); e != nil {
		return e
	}
	if role != "" {
		// 角色名来自部署配置,不是请求数据;仍然走标识符引用,免得以后有人把它接到别处。
		if _, e = tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, schema+"\n"+userDataSchema+"\n"+communitySchema+"\n"+adminSchema+"\n"+translationSchema+"\n"+candidateSkinSchema+"\n"+communityCandidateSkinSchema+"\n"+communityPluginSchema+"\n"+adminOpsSchema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Ready(ctx context.Context) error {
	var n int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM auth_users u
 LEFT JOIN user_preferences p ON p.user_id=u.id
 LEFT JOIN user_clipboard_settings cs ON cs.user_id=u.id
 LEFT JOIN user_clipboard c ON c.user_id=u.id
 LEFT JOIN user_dictionary_state ds ON ds.user_id=u.id
 LEFT JOIN user_dictionary_entries de ON de.user_id=u.id
 LEFT JOIN user_dictionary_changes dc ON dc.user_id=u.id
 LEFT JOIN user_dictionary_overlay ov ON ov.user_id=u.id
 LEFT JOIN user_candidate_positions cp ON cp.user_id=u.id
 LEFT JOIN user_candidate_selections sc ON sc.user_id=u.id
 LEFT JOIN community_resources cr ON cr.owner_id=u.id
 LEFT JOIN community_resource_saves sv ON sv.user_id=u.id
 LEFT JOIN community_resource_ratings rr ON rr.user_id=u.id
 LEFT JOIN community_skins sk ON sk.owner_id=u.id
 LEFT JOIN community_skin_downloads sd ON sd.user_id=u.id
 LEFT JOIN community_skin_ratings sr ON sr.user_id=u.id
 LEFT JOIN translation_cache tc ON false
 LEFT JOIN candidate_skins ck ON false
 LEFT JOIN candidate_skin_resources ckr ON false
 LEFT JOIN community_candidate_skins ccs ON ccs.owner_id=u.id
 LEFT JOIN community_candidate_skin_files ccf ON false
 LEFT JOIN community_candidate_skin_downloads ccd ON ccd.user_id=u.id
 LEFT JOIN community_candidate_skin_ratings ccr ON ccr.user_id=u.id
 LEFT JOIN community_plugins cpl ON cpl.owner_id=u.id
 LEFT JOIN community_plugin_downloads cpd ON cpd.user_id=u.id
 LEFT JOIN community_plugin_ratings cpr ON cpr.user_id=u.id
 LEFT JOIN auth_identities ai ON false AND ai.email_verified AND ai.email||ai.name||ai.picture='' AND ai.updated_at IS NULL
 LEFT JOIN auth_challenges ch ON false AND ch.code_verifier||ch.redirect_uri=''
 LEFT JOIN auth_provider_tokens pt ON false WHERE false`).Scan(&n); e != nil {
		return e
	}
	return s.consoleReady(ctx)
}

// consoleReady probes what the admin console added to tables and paths that run whether or not the admin host is enabled: community moderation and reports, bans, session user agents, dictionary submissions, the extended telemetry columns and kinds, crash groups, public notices and sensitive words. Any of them missing sends startup through the migration.
func (s *Store) consoleReady(ctx context.Context) error {
	if _, e := s.pool.Exec(ctx, `SELECT moderation,previous_moderation,moderation_reason,moderated_by,moderated_at FROM community_skins WHERE false;
SELECT moderation,previous_moderation,moderation_reason,moderated_by,moderated_at FROM community_resources WHERE false;
SELECT moderation,previous_moderation,moderation_reason,moderated_by,moderated_at FROM community_candidate_skins WHERE false;
SELECT moderation,previous_moderation,moderation_reason,moderated_by,moderated_at FROM community_plugins WHERE false;
SELECT id,kind,item_id,reporter_id,reason,detail,created_at FROM community_reports WHERE false;
SELECT banned_at,ban_reason,banned_by FROM auth_users WHERE false;
SELECT user_agent FROM auth_sessions WHERE false;
SELECT id,pr_number,kind,entries,note,created_at FROM word_submissions WHERE false;
SELECT artifact,channel,install_id,signature FROM admin_events WHERE false;
SELECT signature,platform,version,title,status,issue_url,first_seen,last_seen FROM admin_crash_groups WHERE false;
SELECT id,title,body,targets,channels,status,created_by,created_at,published_at,updated_at FROM admin_notices WHERE false;
SELECT id,pattern,is_regex,category,level,created_by,created_at FROM admin_sensitive_words WHERE false;
SELECT word_id,day,count FROM admin_sensitive_hits WHERE false`); e != nil {
		return e
	}
	// The telemetry kinds live in a CHECK constraint, which no SELECT can probe.
	var current bool
	if e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='admin_events'::regclass AND conname='admin_events_kind_check' AND pg_get_constraintdef(oid) LIKE '%session_crash%')`).Scan(&current); e != nil {
		return e
	}
	if !current {
		return errors.New("admin_events kind constraint predates the active and session kinds")
	}
	return nil
}
func (s *Store) Rate(ctx context.Context, key string, limit int, window time.Duration) error {
	var n int
	e := s.pool.QueryRow(ctx, `INSERT INTO auth_rates(key,count,expires_at) VALUES($1,1,now()+$2::interval)
 ON CONFLICT(key) DO UPDATE SET count=CASE WHEN auth_rates.expires_at<=now() THEN 1 ELSE least(auth_rates.count+1,$3+1) END,
 expires_at=CASE WHEN auth_rates.expires_at<=now() THEN excluded.expires_at ELSE auth_rates.expires_at END RETURNING count`, key, window.String(), limit).Scan(&n)
	if e != nil {
		return e
	}
	if n > limit {
		return ErrLimited
	}
	return nil
}
func (s *Store) PutChallenge(ctx context.Context, c Challenge) error {
	_, e := s.pool.Exec(ctx, `INSERT INTO auth_challenges(id_hash,provider,subject,nonce,code_hash,link_user,code_verifier,redirect_uri,expires_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,now()+interval '5 minutes')`, c.IDHash, c.Provider, c.Subject, c.Nonce, c.CodeHash, c.LinkUser, c.CodeVerifier, c.RedirectURI)
	return e
}
func (s *Store) Attempt(ctx context.Context, id string) (Challenge, error) {
	var c Challenge
	e := s.pool.QueryRow(ctx, `UPDATE auth_challenges SET attempts=attempts+1 WHERE id_hash=$1 AND expires_at>now() AND attempts<5
 RETURNING id_hash,provider,subject,nonce,code_hash,COALESCE(link_user,''),code_verifier,redirect_uri,attempts`, hash(id)).Scan(&c.IDHash, &c.Provider, &c.Subject, &c.Nonce, &c.CodeHash, &c.LinkUser, &c.CodeVerifier, &c.RedirectURI, &c.Attempts)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrInvalid
	}
	return c, e
}
func (s *Store) DropChallenge(ctx context.Context, idHash string) {
	s.pool.Exec(ctx, "DELETE FROM auth_challenges WHERE id_hash=$1", idHash)
}
func (s *Store) Complete(ctx context.Context, c Challenge, identity Identity) (Tokens, error) {
	return s.completeWith(ctx, c, identity, nil)
}

// completeWith consumes the challenge, resolves or creates the user and, when grant is set, records the provider profile and sealed refresh token in the same transaction.
func (s *Store) completeWith(ctx context.Context, c Challenge, identity Identity, grant *providerGrant) (Tokens, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Tokens{}, e
	}
	defer tx.Rollback(ctx)
	var id string
	e = tx.QueryRow(ctx, `DELETE FROM auth_challenges WHERE id_hash=$1 AND expires_at>now() RETURNING id_hash`, c.IDHash).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return Tokens{}, ErrInvalid
	}
	if e != nil {
		return Tokens{}, e
	}
	// 同一身份的首次注册与绑定串行化，避免创建重复用户。
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", identity.Provider+":"+identity.Subject); e != nil {
		return Tokens{}, e
	}
	var uid string
	e = tx.QueryRow(ctx, "SELECT user_id FROM auth_identities WHERE provider=$1 AND subject=$2", identity.Provider, identity.Subject).Scan(&uid)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Tokens{}, e
	}
	if c.LinkUser != "" && uid != "" && uid != c.LinkUser {
		return Tokens{}, ErrConflict
	}
	account := uid
	if account == "" {
		account = c.LinkUser
	}
	if account != "" {
		// A banned account can neither sign in nor gain identities. The challenge is still consumed, so the verified credential cannot be replayed. FOR SHARE waits for a ban committing concurrently (it holds the row FOR UPDATE), so a login racing a ban either sees the ban or creates its session before the ban revokes every session.
		var banned bool
		if e = tx.QueryRow(ctx, "SELECT banned_at IS NOT NULL FROM auth_users WHERE id=$1 FOR SHARE", account).Scan(&banned); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return Tokens{}, e
		}
		if banned {
			if e = tx.Commit(ctx); e != nil {
				return Tokens{}, e
			}
			return Tokens{}, ErrBanned
		}
	}
	if uid == "" {
		uid = c.LinkUser
		if uid == "" {
			uid = randomToken()
			if _, e = tx.Exec(ctx, "INSERT INTO auth_users(id,display_name) VALUES($1,$2)", uid, defaultUserName(uid)); e != nil {
				return Tokens{}, e
			}
		}
		if _, e = tx.Exec(ctx, "INSERT INTO auth_identities(provider,subject,user_id) VALUES($1,$2,$3)", identity.Provider, identity.Subject, uid); e != nil {
			return Tokens{}, e
		}
	}
	if grant != nil && grant.Profile != nil {
		p := grant.Profile
		if _, e = tx.Exec(ctx, "UPDATE auth_identities SET email=$3,email_verified=$4,name=$5,picture=$6,updated_at=now() WHERE provider=$1 AND subject=$2", identity.Provider, identity.Subject, p.Email, p.EmailVerified, p.Name, p.Picture); e != nil {
			return Tokens{}, e
		}
	}
	// Google omits refresh_token on repeat consents; an absent one keeps the stored token rather than erasing it.
	if grant != nil && len(grant.SealedRefresh) > 0 {
		if _, e = tx.Exec(ctx, `INSERT INTO auth_provider_tokens(provider,subject,refresh_token,scope,updated_at) VALUES($1,$2,$3,$4,now())
 ON CONFLICT(provider,subject) DO UPDATE SET refresh_token=excluded.refresh_token,scope=excluded.scope,updated_at=now()`, identity.Provider, identity.Subject, grant.SealedRefresh, grant.Scope); e != nil {
			return Tokens{}, e
		}
	}
	t, e := newSession(ctx, tx, uid)
	if e != nil {
		return Tokens{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Tokens{}, e
	}
	return t, nil
}
func newSession(ctx context.Context, tx pgx.Tx, uid string) (Tokens, error) {
	t := Tokens{AccessToken: randomToken(), RefreshToken: randomToken(), TokenType: "Bearer", ExpiresIn: 900}
	e := tx.QueryRow(ctx, "SELECT id,COALESCE(NULLIF(btrim(display_name),''),'水杉小鹿·'||upper(left(id,6))),created_at FROM auth_users WHERE id=$1", uid).Scan(&t.User.ID, &t.User.DisplayName, &t.User.CreatedAt)
	if e != nil {
		return t, e
	}
	// The login request's User-Agent, when the handler passed one, lets the console tell the user's devices apart.
	_, e = tx.Exec(ctx, `INSERT INTO auth_sessions(id,user_id,access_hash,refresh_hash,access_expires,expires_at,user_agent) VALUES($1,$2,$3,$4,now()+interval '15 minutes',now()+interval '30 days',$5)`, randomToken(), uid, hash(t.AccessToken), hash(t.RefreshToken), sessionUserAgent(ctx))
	return t, e
}
func (s *Store) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	if len(token) != 64 {
		return p, ErrInvalid
	}
	var banned bool
	e := s.pool.QueryRow(ctx, `SELECT s.user_id,s.id,s.created_at,u.banned_at IS NOT NULL FROM auth_sessions s JOIN auth_users u ON u.id=s.user_id WHERE s.access_hash=$1 AND NOT s.revoked AND s.access_expires>now() AND s.expires_at>now()`, hash(token)).Scan(&p.UserID, &p.SessionID, &p.CreatedAt, &banned)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrInvalid
	}
	if e == nil && banned {
		// Banning revokes every session in the same transaction; this covers a ban written outside the console.
		return Principal{}, ErrBanned
	}
	return p, e
}
func (s *Store) Refresh(ctx context.Context, token string) (Tokens, error) {
	if len(token) != 64 {
		return Tokens{}, ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Tokens{}, e
	}
	defer tx.Rollback(ctx)
	var sid, uid string
	var revoked, banned bool
	e = tx.QueryRow(ctx, `SELECT s.id,s.user_id,s.revoked,u.banned_at IS NOT NULL FROM auth_sessions s JOIN auth_users u ON u.id=s.user_id WHERE s.refresh_hash=$1 AND s.expires_at>now() FOR UPDATE OF s`, hash(token)).Scan(&sid, &uid, &revoked, &banned)
	if e == nil && banned {
		// The ban revoked this session; the client learns why instead of being sent back to a login that would fail the same way.
		return Tokens{}, ErrBanned
	}
	if e == nil && revoked {
		e = pgx.ErrNoRows
	}
	if errors.Is(e, pgx.ErrNoRows) {
		// 已轮换令牌被重放时撤销该会话族，已签发的新 access_token 也立即失效。
		_, e = tx.Exec(ctx, "UPDATE auth_sessions SET revoked=true WHERE id=(SELECT session_id FROM auth_used_refresh WHERE hash=$1)", hash(token))
		if e != nil {
			return Tokens{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return Tokens{}, e
		}
		return Tokens{}, ErrInvalid
	}
	if e != nil {
		return Tokens{}, e
	}
	t := Tokens{AccessToken: randomToken(), RefreshToken: randomToken(), TokenType: "Bearer", ExpiresIn: 900}
	if _, e = tx.Exec(ctx, "INSERT INTO auth_used_refresh(hash,session_id) VALUES($1,$2)", hash(token), sid); e != nil {
		return t, e
	}
	if _, e = tx.Exec(ctx, "UPDATE auth_sessions SET access_hash=$1,refresh_hash=$2,access_expires=least(now()+interval '15 minutes',expires_at) WHERE id=$3", hash(t.AccessToken), hash(t.RefreshToken), sid); e != nil {
		return t, e
	}
	e = tx.QueryRow(ctx, "SELECT id,COALESCE(NULLIF(btrim(display_name),''),'水杉小鹿·'||upper(left(id,6))),created_at FROM auth_users WHERE id=$1", uid).Scan(&t.User.ID, &t.User.DisplayName, &t.User.CreatedAt)
	if e != nil {
		return t, e
	}
	if e = tx.QueryRow(ctx, "SELECT greatest(0,floor(extract(epoch FROM access_expires-now())))::int FROM auth_sessions WHERE id=$1", sid).Scan(&t.ExpiresIn); e != nil {
		return t, e
	}
	return t, tx.Commit(ctx)
}
func (s *Store) Logout(ctx context.Context, p Principal, all bool) error {
	if all {
		_, e := s.pool.Exec(ctx, "UPDATE auth_sessions SET revoked=true WHERE user_id=$1", p.UserID)
		return e
	}
	_, e := s.pool.Exec(ctx, "UPDATE auth_sessions SET revoked=true WHERE id=$1", p.SessionID)
	return e
}
func (s *Store) Me(ctx context.Context, uid string) (User, []Identity, error) {
	var u User
	ids := []Identity{}
	e := s.pool.QueryRow(ctx, "SELECT id,COALESCE(NULLIF(btrim(display_name),''),'水杉小鹿·'||upper(left(id,6))),created_at FROM auth_users WHERE id=$1", uid).Scan(&u.ID, &u.DisplayName, &u.CreatedAt)
	if e != nil {
		return u, ids, e
	}
	rows, e := s.pool.Query(ctx, "SELECT provider,subject FROM auth_identities WHERE user_id=$1 ORDER BY provider,subject", uid)
	if e != nil {
		return u, ids, e
	}
	defer rows.Close()
	for rows.Next() {
		var i Identity
		if e = rows.Scan(&i.Provider, &i.Subject); e != nil {
			return u, ids, e
		}
		ids = append(ids, i)
	}
	return u, ids, rows.Err()
}
func defaultUserName(uid string) string {
	return "水杉小鹿·" + strings.ToUpper(uid[:min(6, len(uid))])
}
func (s *Store) UpdateName(ctx context.Context, uid, name string) error {
	if strings.TrimSpace(name) == "" {
		name = defaultUserName(uid)
	}
	_, e := s.pool.Exec(ctx, "UPDATE auth_users SET display_name=$1 WHERE id=$2", name, uid)
	return e
}
func (s *Store) DeleteUser(ctx context.Context, uid string) error {
	_, e := s.pool.Exec(ctx, "DELETE FROM auth_users WHERE id=$1", uid)
	return e
}
func (s *Store) Prune(ctx context.Context) {
	for _, q := range []string{"DELETE FROM admin_login_flows WHERE expires_at<now()", "DELETE FROM admin_sessions WHERE expires_at<now()", "DELETE FROM admin_tokens WHERE expires_at<now()", "DELETE FROM auth_challenges WHERE expires_at<now()", "DELETE FROM auth_rates WHERE expires_at<now()", "DELETE FROM auth_sessions WHERE expires_at<now()"} {
		s.pool.Exec(ctx, q)
	}
}
