package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
	"github.com/metasequoiaime/MSIME-Backend/internal/contract"
)

func TestCacheableText(t *testing.T) {
	for _, text := range []string{"词", "hello", strings.Repeat("a", translationCacheTextBytes)} {
		if !cacheableText(text) {
			t.Fatalf("a short word was refused: %q", text)
		}
	}
	// 整段文本、空串、控制字符都不进缓存。8192 字节的额度是给划词翻译的,那种请求不会被第二个人
	// 原样查一遍,存下来只是把用户打的内容留在服务端。
	for _, text := range []string{"", strings.Repeat("a", translationCacheTextBytes+1), "两\n行", "\x00", "\xff\xfe"} {
		if cacheableText(text) {
			t.Fatalf("an uncacheable text was accepted: %q", text)
		}
	}
	if cacheableGloss("") || cacheableGloss(strings.Repeat("a", translationCacheGlossBytes+1)) || !cacheableGloss("word") {
		t.Fatal("gloss bounds are wrong")
	}
	// 短词的输入上限必须远低于契约允许的输入上限,否则这道闸等于没有。
	if translationCacheTextBytes >= contract.TranslationInputBytes {
		t.Fatal("the cache would take whole paragraphs")
	}
}

func TestMissingTextsDeduplicates(t *testing.T) {
	wanted := []string{"你", "爷", "你", "爸", "爷"}
	missing := missingTexts(wanted, map[string]string{"爸": "dad"})
	// 重复的词只送一次,顺序是首次出现的顺序 —— 上游按下标返回,顺序变了就对不回去。
	if len(missing) != 2 || missing[0] != "你" || missing[1] != "爷" {
		t.Fatalf("dedupe or order is wrong: %v", missing)
	}
	if len(missingTexts(wanted, map[string]string{"你": "you", "爷": "grandpa", "爸": "dad"})) != 0 {
		t.Fatal("a fully cached batch still asked the upstream")
	}
}

func TestMergeTranslationsRestoresRequestOrder(t *testing.T) {
	wanted := []string{"你", "爸", "你", "爷"}
	cached := map[string]string{"爸": "dad"}
	missing := []string{"你", "爷"}
	got := mergeTranslations(wanted, cached, missing, []string{"you", "grandpa"})
	want := []string{"you", "dad", "you", "grandpa"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged order is wrong: %v", got)
		}
	}
}

// 缓存不可用(没开账号功能,或者数据库连不上)时翻译照常工作 —— 缓存是旁路,不是依赖。
func TestTranslationWorksWithoutCache(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Response":{"TargetTextList":["you"]}}`)
	})
	s.config.Translation.Provider = "tencent"
	s.config.Translation.secretID = "test-id"
	if w := call(s, "POST", "/v1/translate", `{"text":"你","source_lang":"ZH","target_lang":"EN"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"data":"you"`) {
		t.Fatalf("translation failed without a cache: %d %s", w.Code, w.Body.String())
	}
}

// 一批里重复的词只送上游一次,响应仍然按请求原序逐条对齐。
// 译文等于原文的不入库。上游认不出来就原样返回,这种条目命中了也给不出任何信息,只是白占一行和一个
// 主键。生产上实测 752 行里有 44 行是这种,`bag→bag`、`for→for` 这类。
func TestIdenticalTranslationIsNotCacheable(t *testing.T) {
	if cacheablePair("bag", "bag") || cacheablePair("for", "for") {
		t.Fatal("a gloss identical to its source was accepted")
	}
	if !cacheablePair("包", "bag") {
		t.Fatal("a real gloss was refused")
	}
	// 既有的两道闸仍然生效。
	if cacheablePair("词", "") || cacheablePair("", "gloss") ||
		cacheablePair(strings.Repeat("a", translationCacheTextBytes+1), "gloss") ||
		cacheablePair("词", strings.Repeat("a", translationCacheGlossBytes+1)) {
		t.Fatal("the length and emptiness gates stopped working")
	}
}

func TestTranslationDeduplicatesBatchBeforeUpstream(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ SourceTextList []string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.SourceTextList) != 2 {
			t.Errorf("the upstream got duplicates: %v", body.SourceTextList)
		}
		_, _ = io.WriteString(w, `{"Response":{"TargetTextList":["you","dad"]}}`)
	})
	s.config.Translation.Provider = "tencent"
	s.config.Translation.secretID = "test-id"
	w := call(s, "POST", "/v1/translate", `{"texts":["你","爸","你"],"source_lang":"ZH","target_lang":"EN"}`)
	var result struct {
		Data []string `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data) != 3 ||
		result.Data[0] != "you" || result.Data[1] != "dad" || result.Data[2] != "you" {
		t.Fatalf("duplicate handling changed the response: %d %s", w.Code, w.Body.String())
	}
}

// 端到端:第一次打上游并落库,第二次整批命中、完全不碰上游,hit_count 跟着涨。
func TestTranslationCacheServesRepeatsWithoutUpstream(t *testing.T) {
	admin, schema := disposableSchema(t)
	ctx := context.Background()
	db, err := account.Open(ctx, os.Getenv("MSIME_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// 迁移之后 Ready 必须认得这张表,否则线上会悄悄退化成「永远不缓存」。
	if err = db.Ready(ctx); err != nil {
		t.Fatalf("migration did not create the translation cache: %v", err)
	}
	// 这张表只能有主键一个索引。读路径每次命中都 UPDATE hit_count/used_at,任何覆盖到这两列的索引
	// (包括 `WHERE hit_count > 0` 这类部分索引)都会阻断 HOT,把一次读变成写两个索引 + 留一个死元组。
	var extra int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
 WHERE schemaname=$1 AND tablename='translation_cache' AND indexname <> 'translation_cache_pkey'`,
		schema).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if extra != 0 {
		t.Fatalf("translation_cache gained %d index(es) besides its primary key; that blocks HOT updates on every cache hit", extra)
	}

	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct{ SourceTextList []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([]string, len(body.SourceTextList))
		for i, text := range body.SourceTextList {
			out[i] = "gloss-" + text
		}
		payload, _ := json.Marshal(map[string]any{"Response": map[string]any{"TargetTextList": out}})
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	t.Setenv("TEST_UPSTREAM_TOKEN", "provider-secret")
	s, err := New(Config{
		Auth:        account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Clients:     []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
		Translation: TranslationEndpoint{Endpoint: Endpoint{URL: upstream.URL, TokenEnv: "TEST_UPSTREAM_TOKEN"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.CloseAccounts()
	defer s.Close()
	s.client = upstream.Client()
	s.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s.config.Translation.Provider = "tencent"
	s.config.Translation.secretID = "test-id"

	body := `{"texts":["你","爸"],"source_lang":"ZH","target_lang":"EN"}`
	if w := call(s, "POST", "/v1/translate", body); w.Code != 200 {
		t.Fatalf("first translation failed: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one upstream call, got %d", calls.Load())
	}
	w := call(s, "POST", "/v1/translate", body)
	var result struct {
		Data []string `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data) != 2 ||
		result.Data[0] != "gloss-你" || result.Data[1] != "gloss-爸" {
		t.Fatalf("cached translation did not match: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("a fully cached batch still hit the upstream: %d calls", calls.Load())
	}

	// 缓存按内容寻址,不带 user_id:这张表里不该有任何指回某个用户的列。
	var columns int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
 WHERE table_schema=$1 AND table_name='translation_cache' AND column_name LIKE '%user%'`, schema).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("the shared translation cache must not carry user columns")
	}
	var hits int64
	if _, err = admin.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, "SELECT hit_count FROM translation_cache WHERE source_text='你'").Scan(&hits); err != nil {
		t.Fatal(err)
	}
	// 第一次是未命中写入(0),第二次命中加一。这个计数是将来判断「要不要把高频词收进出货词库」的唯一依据。
	if hits != 1 {
		t.Fatalf("hit_count did not follow the lookups: %d", hits)
	}

	// 整段文本不进缓存:重复请求必须每次都打上游。
	long := strings.Repeat("长", translationCacheTextBytes)
	paragraph, _ := json.Marshal(map[string]any{"texts": []string{long}, "source_lang": "ZH", "target_lang": "EN"})
	for i := 0; i < 2; i++ {
		if w := call(s, "POST", "/v1/translate", string(paragraph)); w.Code != 200 {
			t.Fatalf("paragraph translation failed: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("a paragraph was cached: %d calls", calls.Load())
	}
}
