package server

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// 给一个用例开一套一次性 schema,并把 MSIME_TEST_DATABASE_URL 指过去。**不迁移** —— 要不要迁移由
// 用例自己决定,这正是自动迁移相关用例要验的东西。返回管理连接和 schema 名,方便直接查元数据。
func disposableSchema(t *testing.T) (*pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("MSIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要独立测试 PostgreSQL")
	}
	if !strings.Contains(dsn, "msime_auth_test") {
		t.Fatal("只能使用 msime_auth_test 测试数据库")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(ctx) })
	// PostgreSQL 的标识符上限是 63 字节,所以截的是用例名而不是整个串 —— 截尾巴会把时间戳砍掉,
	// 反而制造重名。时间戳用纳秒:同一秒内跑完两个用例是常态,秒级精度会直接撞名。
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	if len(name) > 40 {
		name = name[:40]
	}
	schema := name + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE")
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	t.Setenv("MSIME_TEST_DATABASE_URL", parsed.String())
	return admin, schema
}

// 空库直接起服务就该跑起来:迁移是纯增量、带 advisory lock 的,让运维记得先跑一次 -migrate-users
// 只是把一个可以自动做对的事变成一个会忘的事。
func TestStartupMigratesAnEmptyDatabase(t *testing.T) {
	admin, schema := disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	t.Setenv("TEST_ADMIN_TOKEN", strings.Repeat("q", 48))
	config := func() Config {
		return Config{
			Auth:    account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
			Admin:   AdminConfig{Enabled: true, Host: "admin.example.com", TokenEnv: "TEST_ADMIN_TOKEN"},
			Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
		}
	}
	// 这里没有任何 Migrate 调用。
	s, err := New(config())
	if err != nil {
		t.Fatalf("an empty database did not migrate itself at startup: %v", err)
	}
	// 用户表、社区表、管理后台表和译文缓存都归同一次迁移管,少一张都说明自动迁移漏了一段 schema。
	var tables int
	if err = admin.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables
 WHERE table_schema=$1 AND table_name IN
 ('auth_users','user_preferences','user_dictionary_entries','community_skins','admin_members','translation_cache')`,
		schema).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	s.CloseAccounts()
	s.Close()
	if tables != 6 {
		t.Fatalf("startup migration created %d of 6 expected tables", tables)
	}

	// 再起一次。迁移必须可重复执行 —— 多副本滚动升级时每个副本都会走这条路。
	again, err := New(config())
	if err != nil {
		t.Fatalf("starting against an already migrated database failed: %v", err)
	}
	again.CloseAccounts()
	again.Close()
}

// 用户表在、管理后台表不在:管理后台是后加的,这种库能通过 Ready 却卡在 AdminReady。
func TestStartupMigratesAdminTablesAddedLater(t *testing.T) {
	admin, schema := disposableSchema(t)
	ctx := context.Background()
	db, err := account.Open(ctx, os.Getenv("MSIME_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db.Close()
	quoted := pgx.Identifier{schema}.Sanitize()
	for _, table := range []string{"admin_members", "admin_events", "admin_audit", "admin_login_flows", "admin_sessions"} {
		if _, err = admin.Exec(ctx, "DROP TABLE "+quoted+"."+pgx.Identifier{table}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	t.Setenv("TEST_ADMIN_TOKEN", strings.Repeat("q", 48))
	s, err := New(Config{
		Auth:    account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Admin:   AdminConfig{Enabled: true, Host: "admin.example.com", TokenEnv: "TEST_ADMIN_TOKEN"},
		Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
	})
	if err != nil {
		t.Fatalf("missing admin tables were not migrated at startup: %v", err)
	}
	s.CloseAccounts()
	s.Close()
}
