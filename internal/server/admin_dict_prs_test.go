package server

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
	"github.com/metasequoiaime/MSIME-Backend/internal/githubapp"
)

const dictTestRepo = "metasequoiaime/msime-dictionary"

// fakeDictPull is one pull request of the fake dictionary repository.
type fakeDictPull struct {
	number      int
	title       string
	state       string
	merged      bool
	ref         string
	head        string
	base        string
	fork        bool
	user        string
	comments    []string
	mergeSHA    string
	mergeTitle  string
	mergeMethod string
}

// fakeDictRepo stands in for the GitHub REST API of the dictionary repository: commits are file maps, branches point at commits, and contents writes are compare-and-swaps on the blob SHA like the real API.
type fakeDictRepo struct {
	t       *testing.T
	mu      sync.Mutex
	server  *httptest.Server
	commits map[string]map[string]string
	pulls   []*fakeDictPull
	seq     int
	calls   []string
	status  map[string]int
}

func blobSHA(content string) string {
	sum := sha1.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

const (
	dictTestBaseWords        = "未来可期\twei'lai'ke'qi\t1\n今天\tjin'tian\t9000\n"
	dictTestBaseEnglish      = "asr\tASR\t1\n"
	dictTestBaseTranslations = "# zh -> en\n苹果\tapple\n"
)

// newFakeDictRepo starts with base commit "base1" and these pull requests: #12 open rolling (7 added entries across the three files, including a second copy of a base translation), #11 merged, #10 closed, #9 from a fork and #8 on another branch, which the console must ignore.
func newFakeDictRepo(t *testing.T) *fakeDictRepo {
	f := &fakeDictRepo{t: t, commits: map[string]map[string]string{}, status: map[string]int{}}
	f.commits["base1"] = map[string]string{"custom/words.txt": dictTestBaseWords, "custom/english.txt": dictTestBaseEnglish, "custom/translations.txt": dictTestBaseTranslations}
	f.commits["head12"] = map[string]string{
		"custom/words.txt":        dictTestBaseWords + "水杉\tshui'shan\t5000\n今天\tjin'tian\t100\n坏\tbad'pin\t5000\n水杉\tshui'shan\t5000\n",
		"custom/english.txt":      dictTestBaseEnglish + "msime\tMSIME\t1\n",
		"custom/translations.txt": dictTestBaseTranslations + "苹果\tapple\n水杉\tmetasequoia\n",
	}
	f.commits["head11"] = map[string]string{"custom/words.txt": dictTestBaseWords + "江汉\tjiang'han\t5000\n", "custom/english.txt": dictTestBaseEnglish, "custom/translations.txt": dictTestBaseTranslations}
	f.commits["head10"] = f.commits["head11"]
	f.pulls = []*fakeDictPull{
		{number: 12, title: "feat(custom): add 4 words, 1 English word and 1 translation", state: "open", ref: "community-words/20260930-120000", head: "head12", base: "base1", user: "msime-words[bot]"},
		{number: 11, title: "feat(custom): add 1 word", state: "closed", merged: true, ref: "community-words/20260920-120000", head: "head11", base: "base1", user: "msime-words[bot]"},
		{number: 10, title: "feat(custom): add 1 word", state: "closed", ref: "community-words/20260910-120000", head: "head10", base: "base1", user: "msime-words[bot]"},
		{number: 9, title: "fork", state: "open", ref: "community-words/evil", head: "head10", base: "base1", fork: true, user: "someone"},
		{number: 8, title: "chore: other", state: "open", ref: "feature/x", head: "head10", base: "base1", user: "maintainer"},
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDictRepo) pull(n int) *fakeDictPull {
	for _, p := range f.pulls {
		if p.number == n {
			return p
		}
	}
	return nil
}

func (f *fakeDictRepo) pullJSON(p *fakeDictPull) map[string]any {
	repo := dictTestRepo
	if p.fork {
		repo = "someone/msime-dictionary"
	}
	created := time.Date(2026, 9, p.number, 12, 0, 0, 0, time.UTC)
	var merged any
	if p.merged {
		merged = created.Add(time.Hour)
	}
	return map[string]any{"number": p.number, "title": p.title, "state": p.state, "html_url": "https://github.com/" + dictTestRepo + "/pull/" + strconv.Itoa(p.number),
		"created_at": created, "updated_at": created, "merged_at": merged, "mergeable": true,
		"user": map[string]any{"login": p.user, "type": map[bool]string{true: "Bot", false: "User"}[strings.HasSuffix(p.user, "[bot]")]},
		"head": map[string]any{"ref": p.ref, "sha": p.head, "repo": map[string]any{"full_name": repo}},
		"base": map[string]any{"ref": "main", "sha": p.base}}
}

func (f *fakeDictRepo) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeDictRepo) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	if status, ok := f.status[key]; ok {
		f.reply(w, status, map[string]string{"message": "forced"})
		return
	}
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	str := func(k string) string { v, _ := body[k].(string); return v }
	if key == "POST /app/installations/77/access_tokens" {
		f.reply(w, 201, map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour)})
		return
	}
	if r.Header.Get("Authorization") != "Bearer installation-token" {
		f.reply(w, 401, map[string]string{"message": "bad token"})
		return
	}
	prefix := "/repos/" + dictTestRepo
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case r.Method == "GET" && path == "/pulls":
		if q := r.URL.Query(); q.Get("state") != "all" || q.Get("per_page") != "100" {
			f.t.Errorf("unexpected list query %s", r.URL.RawQuery)
		}
		out := []map[string]any{}
		for _, p := range f.pulls {
			out = append(out, f.pullJSON(p))
		}
		f.reply(w, 200, out)
	case strings.HasPrefix(path, "/pulls/"):
		rest := strings.Split(strings.TrimPrefix(path, "/pulls/"), "/")
		n, _ := strconv.Atoi(rest[0])
		p := f.pull(n)
		if p == nil {
			f.reply(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		switch {
		case r.Method == "GET" && len(rest) == 1:
			f.reply(w, 200, f.pullJSON(p))
		case r.Method == "PATCH" && len(rest) == 1:
			if t := str("title"); t != "" {
				p.title = t
			}
			if s := str("state"); s != "" {
				p.state = s
			}
			f.reply(w, 200, f.pullJSON(p))
		case r.Method == "PUT" && len(rest) == 2 && rest[1] == "merge":
			if str("sha") != p.head {
				f.reply(w, 409, map[string]string{"message": "Head branch was modified"})
				return
			}
			p.merged, p.state, p.mergeSHA, p.mergeTitle, p.mergeMethod = true, "closed", str("sha"), str("commit_title"), str("merge_method")
			f.reply(w, 200, map[string]any{"merged": true, "sha": "merged-" + p.head})
		default:
			f.reply(w, 404, map[string]string{"message": "Not Found"})
		}
	case r.Method == "POST" && strings.HasPrefix(path, "/issues/") && strings.HasSuffix(path, "/comments"):
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path, "/issues/"), "/comments"))
		p := f.pull(n)
		p.comments = append(p.comments, str("body"))
		f.reply(w, 201, map[string]any{"id": 1})
	case strings.HasPrefix(path, "/contents/"):
		file := strings.TrimPrefix(path, "/contents/")
		if r.Method == "GET" {
			ref := r.URL.Query().Get("ref")
			for _, p := range f.pulls {
				if p.ref == ref {
					ref = p.head
				}
			}
			content, ok := f.commits[ref][file]
			if !ok {
				f.reply(w, 404, map[string]string{"message": "Not Found"})
				return
			}
			f.reply(w, 200, map[string]any{"type": "file", "encoding": "base64", "sha": blobSHA(content), "content": base64.StdEncoding.EncodeToString([]byte(content))})
			return
		}
		var p *fakeDictPull
		for _, candidate := range f.pulls {
			if candidate.ref == str("branch") {
				p = candidate
			}
		}
		if r.Method != "PUT" || p == nil {
			f.reply(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		if blobSHA(f.commits[p.head][file]) != str("sha") {
			f.reply(w, 409, map[string]string{"message": "sha does not match"})
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(str("content"))
		f.seq++
		next := "c" + strconv.Itoa(f.seq)
		files := map[string]string{}
		for k, v := range f.commits[p.head] {
			files[k] = v
		}
		files[file] = string(raw)
		f.commits[next], p.head = files, next
		f.reply(w, 200, map[string]any{"commit": map[string]any{"sha": next}})
	default:
		f.reply(w, 404, map[string]string{"message": "Not Found"})
	}
}

func (f *fakeDictRepo) wrote() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var writes []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") && !strings.HasPrefix(c, "POST /app/") {
			writes = append(writes, c)
		}
	}
	return writes
}

func dictGitHubClient(t *testing.T, f *fakeDictRepo) *githubapp.Client {
	return &githubapp.Client{AppID: 42, InstallationID: 77, Key: wordsKey(t), APIURL: f.server.URL, HTTP: f.server.Client(), Cache: &githubapp.Cache{}}
}

const dictTestAdminToken = "dict-admin-token-dict-admin-token-dict-admin"

// dictPRServer is a database-backed admin server (host admin.example.com, legacy token dictTestAdminToken) whose console GitHub client talks to a fake dictionary repository. It returns a connection to the test database and the schema holding this test's tables.
func dictPRServer(t *testing.T) (*Server, *fakeDictRepo, *pgx.Conn, string) {
	t.Helper()
	conn, schema := disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	t.Setenv("TEST_ADMIN_TOKEN", dictTestAdminToken)
	s, err := New(Config{
		Auth:    account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Admin:   AdminConfig{Enabled: true, Host: "admin.example.com", TokenEnv: "TEST_ADMIN_TOKEN"},
		Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.CloseAccounts()
		s.Close()
	})
	f := newFakeDictRepo(t)
	s.config.Admin.GitHub.DictionaryRepo = dictTestRepo
	s.adminGitHub = dictGitHubClient(t, f)
	return s, f, conn, schema
}

func dictCall(s *Server, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "https://admin.example.com"+path, reader)
	r.Header.Set("Authorization", "Bearer "+dictTestAdminToken)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

type dictAuditRow struct{ action, target, count, removed, reason, actor string }

func dictAudit(t *testing.T, conn *pgx.Conn, schema string) []dictAuditRow {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT action, target, coalesce(detail->>'count',''), coalesce(detail->>'removed',''), coalesce(detail->>'reason',''), actor FROM `+pgx.Identifier{schema, "admin_audit"}.Sanitize()+` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []dictAuditRow
	for rows.Next() {
		var row dictAuditRow
		if err = rows.Scan(&row.action, &row.target, &row.count, &row.removed, &row.reason, &row.actor); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func decodeDict[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	return v
}

type dictListBody struct {
	Repo   string          `json:"repo"`
	Items  []dictPRSummary `json:"items"`
	Counts map[string]int  `json:"counts"`
}

type dictDetailBody struct {
	Pull    dictPRSummary `json:"pull"`
	HeadSHA string        `json:"head_sha"`
	Entries []dictEntry   `json:"entries"`
}

// Without admin.github the review page shows 未配置, and writes are refused the same way before anything else.
func TestDictPRsGitHubDisabled(t *testing.T) {
	s, _ := adminRBACFixture(t)
	for _, tc := range []struct{ method, path string }{{"GET", "/api/dict-prs"}, {"GET", "/api/dict-prs/12"}, {"POST", "/api/dict-prs/12/approve"}} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, adminRequest(s, tc.method, tc.path, "", strings.Repeat("a", 40)))
		if w.Code != 404 || !strings.Contains(w.Body.String(), "github_disabled") {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	if pending, err := s.pendingDictPRs(context.Background()); pending != 0 || err != nil {
		t.Fatal(pending, err)
	}
}

// Review writes need review_dict_pr; a role without it reaches no GitHub write.
func TestDictPRWritesRequirePermission(t *testing.T) {
	s, _ := adminRBACFixture(t)
	f := newFakeDictRepo(t)
	s.config.Admin.GitHub.DictionaryRepo = dictTestRepo
	s.adminGitHub = dictGitHubClient(t, f)
	for _, action := range []string{"approve", "reject", "trim"} {
		r := adminRequest(s, "POST", "/api/dict-prs/12/"+action, "", account.AdminTokenPrefix+"readonly")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "permission_denied") {
			t.Fatal(action, w.Code, w.Body.String())
		}
	}
	if writes := f.wrote(); len(writes) != 0 {
		t.Fatal("a forbidden review reached GitHub:", writes)
	}
}

func TestDictPRRouting(t *testing.T) {
	s, f, _, _ := dictPRServer(t)
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"POST", "/api/dict-prs", 405, "method_not_allowed"},
		{"DELETE", "/api/dict-prs/12", 405, "method_not_allowed"},
		{"GET", "/api/dict-prs/12/approve", 405, "method_not_allowed"},
		{"GET", "/api/dict-prs/abc", 400, "invalid_id"},
		{"GET", "/api/dict-prs/012", 400, "invalid_id"},
		{"GET", "/api/dict-prs/0", 400, "invalid_id"},
		{"POST", "/api/dict-prs/12/merge", 404, "not_found"},
		{"GET", "/api/dict-prs?state=draft", 400, "invalid_state"},
		// A fork's community-words/ branch and other branches are not website submissions.
		{"GET", "/api/dict-prs/9", 404, "not_found"},
		{"GET", "/api/dict-prs/8", 404, "not_found"},
		{"GET", "/api/dict-prs/404", 404, "not_found"},
	} {
		w := dictCall(s, tc.method, tc.path, "")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal(tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if writes := f.wrote(); len(writes) != 0 {
		t.Fatal(writes)
	}
}

func TestDictPRList(t *testing.T) {
	s, _, conn, schema := dictPRServer(t)
	w := dictCall(s, "GET", "/api/dict-prs", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := decodeDict[dictListBody](t, w)
	if body.Repo != dictTestRepo || body.Counts["open"] != 1 || body.Counts["merged"] != 1 || body.Counts["closed"] != 1 || body.Counts["all"] != 3 || len(body.Items) != 3 {
		t.Fatalf("%+v", body)
	}
	open := body.Items[0]
	if open.Number != 12 || open.State != "open" || !open.AuthorBot || open.Counts == nil || *open.Counts != (dictPRCounts{Total: 7, New: 3, Dup: 3, Flagged: 1}) {
		t.Fatalf("%+v %+v", open, open.Counts)
	}
	if body.Items[1].State != "merged" || body.Items[2].State != "closed" || body.Items[1].Counts != nil {
		t.Fatalf("%+v", body.Items)
	}
	w = dictCall(s, "GET", "/api/dict-prs?state=merged", "")
	if body = decodeDict[dictListBody](t, w); len(body.Items) != 1 || body.Items[0].Number != 11 || body.Counts["all"] != 3 {
		t.Fatalf("%+v", body)
	}
	// The open pull request is announced once, and the list feeds the global search and the shell badge.
	if st := s.dictPRState(); !st.notified[12] || st.notified[11] || st.notified[9] {
		t.Fatal(st.notified)
	}
	if got := notificationRows(t, conn, schema); !slices.Equal(got, []string{"dict_pr dictpr 12"}) {
		t.Fatal("dictionary pull request notifications", got)
	}
	if hits := s.searchDictPRs("#12"); len(hits) != 1 || hits[0].ID != "12" || hits[0].Target != "dictpr" || hits[0].Kind != "dict_pr" {
		t.Fatalf("%+v", hits)
	}
	if hits := s.searchDictPRs("FEAT(CUSTOM)"); len(hits) != 3 {
		t.Fatalf("%+v", hits)
	}
	if hits := s.searchDictPRs("  "); hits != nil {
		t.Fatal(hits)
	}
	if pending, err := s.pendingDictPRs(context.Background()); pending != 1 || err != nil {
		t.Fatal(pending, err)
	}
}

func TestDictPRDetailFlags(t *testing.T) {
	s, _, _, _ := dictPRServer(t)
	// The shipped English dictionary already has msime; the words batch answers not listed.
	s.config.Engine.Binary = fakeEngine(t, `request=$(cat); case "$request" in *listed_english_batch*) echo '{"listed":[true]}';; *) echo '{"listed":[false,false,false]}';; esac`)
	w := dictCall(s, "GET", "/api/dict-prs/12", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := decodeDict[dictDetailBody](t, w)
	want := []struct{ kind, word, second, flag string }{
		{"words", "水杉", "shui'shan", dictFlagNew},
		{"words", "今天", "jin'tian", dictFlagDup},
		{"words", "坏", "bad'pin", dictFlagBad},
		{"words", "水杉", "shui'shan", dictFlagDup},
		{"english", "msime", "MSIME", dictFlagDup},
		{"translations", "苹果", "apple", dictFlagDup},
		{"translations", "水杉", "metasequoia", dictFlagNew},
	}
	if len(body.Entries) != len(want) || body.HeadSHA != "head12" || body.Pull.Number != 12 || *body.Pull.Counts != (dictPRCounts{Total: 7, New: 2, Dup: 4, Flagged: 1}) {
		t.Fatalf("%+v", body)
	}
	for i, e := range body.Entries {
		if e.Index != i || e.Kind != want[i].kind || e.Word != want[i].word || e.Pinyin != want[i].second || e.Flag != want[i].flag {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}
	if body.Entries[2].Reason == "" || body.Entries[1].Reason != kindWords.listed || body.Entries[4].Reason != kindEnglish.listed || body.Entries[5].Reason != kindTranslations.listed {
		t.Fatalf("%+v", body.Entries)
	}
}

func TestDictPRTrim(t *testing.T) {
	s, f, conn, schema := dictPRServer(t)
	for _, body := range []string{`{}`, `{"keep":[0]}`, `{"keep":[],"head_sha":"head12"}`, `{"keep":[99],"head_sha":"head12"}`, `{"keep":[0,0],"head_sha":"head12"}`, `{"keep":[-1],"head_sha":"head12"}`, `{"keep":[0],"head_sha":"head12","extra":1}`} {
		if w := dictCall(s, "POST", "/api/dict-prs/12/trim", body); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if w := dictCall(s, "POST", "/api/dict-prs/12/trim", `{"keep":[0],"head_sha":"stale"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "pr_changed") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A concurrent write to words.txt fails the compare-and-swap; nothing is audited.
	f.status["PUT /repos/"+dictTestRepo+"/contents/custom/words.txt"] = 409
	if w := dictCall(s, "POST", "/api/dict-prs/12/trim", `{"keep":[0,4,6],"head_sha":"head12"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "pr_changed") {
		t.Fatal(w.Code, w.Body.String())
	}
	if rows := dictAudit(t, conn, schema); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
	delete(f.status, "PUT /repos/"+dictTestRepo+"/contents/custom/words.txt")

	w := dictCall(s, "POST", "/api/dict-prs/12/trim", `{"keep":[0,4,6],"head_sha":"head12"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	result := decodeDict[map[string]any](t, w)
	p := f.pull(12)
	if result["count"] != float64(3) || result["removed"] != float64(4) || result["head_sha"] != p.head {
		t.Fatalf("%+v", result)
	}
	files := f.commits[p.head]
	if files["custom/words.txt"] != dictTestBaseWords+"水杉\tshui'shan\t5000\n" || files["custom/english.txt"] != dictTestBaseEnglish+"msime\tMSIME\t1\n" || files["custom/translations.txt"] != dictTestBaseTranslations+"水杉\tmetasequoia\n" {
		t.Fatalf("%q", files)
	}
	if p.title != "feat(custom): add 1 word, 1 English word and 1 translation" || p.state != "open" || p.merged {
		t.Fatalf("%+v", p)
	}
	rows := dictAudit(t, conn, schema)
	if len(rows) != 1 || rows[0] != (dictAuditRow{"dict_pr_trim", "12", "3", "4", "", "legacy-token"}) {
		t.Fatalf("%+v", rows)
	}
	// The reviewer's old head is stale now.
	if w = dictCall(s, "POST", "/api/dict-prs/12/trim", `{"keep":[0],"head_sha":"head12"}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = dictCall(s, "GET", "/api/dict-prs/12", ""); decodeDict[dictDetailBody](t, w).Pull.Counts.Total != 3 {
		t.Fatal(w.Body.String())
	}
}

func TestDictPRApprove(t *testing.T) {
	s, f, conn, schema := dictPRServer(t)
	if w := dictCall(s, "POST", "/api/dict-prs/11/approve", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "not_open") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A merge GitHub refuses leaves the trimmed branch and its audit row, but no approval.
	f.status["PUT /repos/"+dictTestRepo+"/pulls/12/merge"] = 405
	if w := dictCall(s, "POST", "/api/dict-prs/12/approve", `{"keep":[0,1,4,5,6]}`); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_head_sha") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := dictCall(s, "POST", "/api/dict-prs/12/approve", `{"keep":[0,1,4,5,6],"head_sha":"head12"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "not_mergeable") {
		t.Fatal(w.Code, w.Body.String())
	}
	if rows := dictAudit(t, conn, schema); len(rows) != 1 || rows[0].action != "dict_pr_trim" || rows[0].removed != "2" {
		t.Fatalf("%+v", rows)
	}
	delete(f.status, "PUT /repos/"+dictTestRepo+"/pulls/12/merge")

	trimmed := f.pull(12).head
	w := dictCall(s, "POST", "/api/dict-prs/12/approve", `{"keep":[0,2,4],"head_sha":"`+trimmed+`"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := f.pull(12)
	if !p.merged || p.mergeMethod != "squash" || p.mergeSHA != p.head || p.mergeSHA == trimmed || p.mergeTitle != "feat(custom): add 1 word, 1 English word and 1 translation (#12)" {
		t.Fatalf("%+v", p)
	}
	if words := f.commits[p.head]["custom/words.txt"]; words != dictTestBaseWords+"水杉\tshui'shan\t5000\n" {
		t.Fatalf("%q", words)
	}
	rows := dictAudit(t, conn, schema)
	if len(rows) != 3 || rows[1].action != "dict_pr_trim" || rows[2] != (dictAuditRow{"dict_pr_approve", "12", "3", "2", "", "legacy-token"}) {
		t.Fatalf("%+v", rows)
	}
	// Approving without keep merges every entry as it is.
	f.pulls = append(f.pulls, &fakeDictPull{number: 13, title: "feat(custom): add 1 word", state: "open", ref: "community-words/20261001-000000", head: "head11", base: "base1", user: "msime-words[bot]"})
	if w = dictCall(s, "POST", "/api/dict-prs/13/approve", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if p = f.pull(13); !p.merged || p.mergeSHA != "head11" || p.mergeTitle != "feat(custom): add 1 word (#13)" {
		t.Fatalf("%+v", p)
	}
	if rows = dictAudit(t, conn, schema); len(rows) != 4 || rows[3] != (dictAuditRow{"dict_pr_approve", "13", "1", "0", "", "legacy-token"}) {
		t.Fatalf("%+v", rows)
	}
}

func TestDictPRReject(t *testing.T) {
	s, f, conn, schema := dictPRServer(t)
	for _, body := range []string{`{}`, `{"reason":"  "}`, `{"reason":"` + strings.Repeat("长", 501) + `"}`, `{"reason":"a\u0000b"}`} {
		if w := dictCall(s, "POST", "/api/dict-prs/12/reject", body); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_") {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	f.status["POST /repos/"+dictTestRepo+"/issues/12/comments"] = 500
	if w := dictCall(s, "POST", "/api/dict-prs/12/reject", `{"reason":"拼音不规范"}`); w.Code != 502 {
		t.Fatal(w.Code, w.Body.String())
	}
	if p := f.pull(12); p.state != "open" || len(dictAudit(t, conn, schema)) != 0 {
		t.Fatalf("%+v", p)
	}
	delete(f.status, "POST /repos/"+dictTestRepo+"/issues/12/comments")
	if w := dictCall(s, "POST", "/api/dict-prs/12/reject", `{"reason":"含敏感或导流内容：第 3 条"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if p := f.pull(12); p.state != "closed" || p.merged || len(p.comments) != 1 || p.comments[0] != "审核未通过：含敏感或导流内容：第 3 条" {
		t.Fatalf("%+v", p)
	}
	rows := dictAudit(t, conn, schema)
	if len(rows) != 1 || rows[0] != (dictAuditRow{"dict_pr_reject", "12", "", "", "含敏感或导流内容：第 3 条", "legacy-token"}) {
		t.Fatalf("%+v", rows)
	}
	// The list reflects the write at once instead of serving the cached read.
	if body := decodeDict[dictListBody](t, dictCall(s, "GET", "/api/dict-prs", "")); body.Counts["open"] != 0 || body.Counts["closed"] != 2 {
		t.Fatalf("%+v", body.Counts)
	}
	if w := dictCall(s, "POST", "/api/dict-prs/12/reject", `{"reason":"x"}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Website titles only count entries, so the global search also matches the submitter's note and names the pull request by it.
func TestDictPRSearchNotes(t *testing.T) {
	s, _, _, _ := dictPRServer(t)
	st := s.dictPRState()
	st.mu.Lock()
	st.notes[12] = "湖北潜江本地地名"
	st.mu.Unlock()
	if w := dictCall(s, "GET", "/api/dict-prs", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	hits := s.searchDictPRs("潜江")
	if len(hits) != 1 || hits[0].ID != "12" || hits[0].Title != "#12 词库：湖北潜江本地地名" {
		t.Fatalf("%+v", hits)
	}
	if hits = s.searchDictPRs("#11"); len(hits) != 1 || hits[0].Title != "#11 feat(custom): add 1 word" {
		t.Fatalf("%+v", hits)
	}
}
