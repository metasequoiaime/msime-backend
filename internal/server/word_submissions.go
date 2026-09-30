package server

import (
	"bufio"
	"context"
	"crypto/rsa"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// Anonymous word submissions from the website (msime-web#213). A visitor proposes words with their quanpin reading; after Cloudflare Turnstile and a per-address PostgreSQL rate limit, the server appends them to words.txt in metasequoiaime/msime-customdict on a rolling pull request that maintainers review. Nothing about the visitor is stored, and entries, notes and tokens are never logged.

const (
	wordSubmissionsPath      = "/v1/community/word-submissions"
	wordSubmissionBodyBytes  = 16 << 10
	wordSubmissionMaxEntries = 20
	wordSubmissionMaxChars   = 16
	wordSubmissionNoteChars  = 500
	wordSubmissionTokenBytes = 2048
	// Every community entry gets the same weight; submitters do not choose it.
	wordSubmissionWeight   = 5000
	wordSubmissionsFile    = "data/words.txt"
	wordSubmissionBranches = "community-words/"
	wordSubmissionTimeout  = 45 * time.Second
	defaultTurnstileURL    = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	defaultGitHubAPIURL    = "https://api.github.com"
)

// Conservative per-address limits. They count requests that passed Turnstile, so an automated client cannot use up a visitor's quota without solving a challenge. The shorter window is checked first so a burst does not also consume the daily allowance.
var wordSubmissionLimits = []struct {
	scope      string
	limit      int
	window     time.Duration
	retryAfter string
}{
	{"word-submissions-10m", 3, 10 * time.Minute, "600"},
	{"word-submissions-day", 20, 24 * time.Hour, "3600"},
}

// The quanpin syllable table is a copy of msime platforms/windows/installer/assets/tables/pinyin.txt (402 syllables, ü written as v, lüe/nüe as lve/nve), the same list the website form validates against, so the page and the server agree on what a valid reading is.
//
//go:embed pinyin_syllables.txt
var pinyinSyllableTable string

var pinyinSyllables = func() map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(pinyinSyllableTable, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set
}()

type TurnstileConfig struct {
	SiteKey       string `json:"site_key"`
	SecretEnv     string `json:"secret_env"`
	SiteverifyURL string `json:"siteverify_url,omitempty"`
	secret        string
}

type WordsGitHubConfig struct {
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	PrivateKeyEnv  string `json:"private_key_env"`
	Repository     string `json:"repository"`
	Branch         string `json:"branch"`
	APIURL         string `json:"api_url,omitempty"`
	key            *rsa.PrivateKey
}

// WordSubmissionsConfig enables the anonymous website word form. An empty turnstile.site_key keeps the feature off; once it is set every other field is required and the server refuses to start with a partial configuration.
type WordSubmissionsConfig struct {
	// ClientIPHeader names a header set by the trusted reverse proxy that carries the visitor address (for example CF-Connecting-IP or X-Real-IP; for X-Forwarded-For the last entry is used). Empty trusts only the TCP peer, which behind a proxy makes every visitor share one quota. Set it only when the proxy overwrites the header, because clients can send it themselves.
	ClientIPHeader string            `json:"client_ip_header"`
	Turnstile      TurnstileConfig   `json:"turnstile"`
	GitHub         WordsGitHubConfig `json:"github"`
}

func (c WordSubmissionsConfig) enabled() bool { return c.Turnstile.SiteKey != "" }

var (
	githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	githubBranchPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
	headerNamePattern       = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

func (c *WordSubmissionsConfig) validate(authEnabled bool, origins []string) error {
	if !c.enabled() {
		return nil
	}
	if !authEnabled {
		return errors.New("word_submissions requires auth.enabled: the per-address rate limit is stored in PostgreSQL")
	}
	if len(origins) == 0 {
		return errors.New("word_submissions requires allowed_origins: the form is only accepted from the website")
	}
	if c.ClientIPHeader != "" && !headerNamePattern.MatchString(c.ClientIPHeader) {
		return errors.New("word_submissions client_ip_header must be a header name")
	}
	t := &c.Turnstile
	if !validProviderCredential(t.SiteKey) || len(t.SiteKey) > 256 {
		return errors.New("word_submissions turnstile site_key is invalid")
	}
	t.secret = os.Getenv(t.SecretEnv)
	if t.SecretEnv == "" || !validProviderCredential(t.secret) {
		return errors.New("word_submissions turnstile secret_env missing or invalid")
	}
	if t.SiteverifyURL == "" {
		t.SiteverifyURL = defaultTurnstileURL
	}
	if !plainHTTPSURL(t.SiteverifyURL, false) {
		return errors.New("word_submissions turnstile siteverify_url must be an HTTPS URL without query or credentials")
	}
	g := &c.GitHub
	if g.AppID <= 0 || g.InstallationID <= 0 {
		return errors.New("word_submissions github app_id and installation_id are required")
	}
	if !githubRepositoryPattern.MatchString(g.Repository) {
		return errors.New("word_submissions github repository must be owner/name")
	}
	if g.Branch == "" {
		g.Branch = "main"
	}
	if !githubBranchPattern.MatchString(g.Branch) || strings.Contains(g.Branch, "..") || strings.HasPrefix(g.Branch, wordSubmissionBranches) {
		return errors.New("word_submissions github branch is invalid")
	}
	if g.APIURL == "" {
		g.APIURL = defaultGitHubAPIURL
	}
	g.APIURL = strings.TrimSuffix(g.APIURL, "/")
	if !plainHTTPSURL(g.APIURL, true) {
		return errors.New("word_submissions github api_url must be an HTTPS URL without query or credentials")
	}
	key, err := parseGitHubAppKey(os.Getenv(g.PrivateKeyEnv))
	if g.PrivateKeyEnv == "" || err != nil {
		return errors.New("word_submissions github private_key_env must hold the GitHub App RSA private key (PEM, PKCS#1 or PKCS#8)")
	}
	g.key = key
	return nil
}

func plainHTTPSURL(raw string, allowEmptyPath bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && (allowEmptyPath || u.Path != "")
}

// GitHub hands out PKCS#1 keys; converted keys are PKCS#8. Secret stores that cannot hold newlines often store the PEM with literal \n sequences, so those are accepted too.
func parseGitHubAppKey(raw string) (*rsa.PrivateKey, error) {
	if !strings.Contains(raw, "\n") {
		raw = strings.ReplaceAll(raw, `\n`, "\n")
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App keys are RSA")
	}
	return key, nil
}

type rateLimiter interface {
	RateLimit(ctx context.Context, scope, subject string, limit int, window time.Duration) error
}

type wordSubmitter struct {
	config    WordSubmissionsConfig
	origins   []string
	hostnames []string
	limiter   rateLimiter
	client    *http.Client
	now       func() time.Time
	// Serialises the read-modify-write on GitHub within this process so two local requests never race for the same blob SHA. Replicas can still race; GitHub's SHA check turns that into a 409.
	writes      sync.Mutex
	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time
}

func newWordSubmitter(c WordSubmissionsConfig, origins []string, limiter rateLimiter) *wordSubmitter {
	ws := &wordSubmitter{config: c, origins: origins, limiter: limiter, now: time.Now,
		client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil {
			ws.hostnames = append(ws.hostnames, u.Hostname())
		}
	}
	return ws
}

// Machine-readable code plus a message the website can show as is. The plain-string error (rather than the {code,message} object used elsewhere) is the contract the website form reads.
func wordsFail(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": message, "code": code})
}

func (s *Server) wordSubmissionSettings(w http.ResponseWriter, r *http.Request) {
	if s.words == nil {
		respond(w, 200, map[string]any{"enabled": false, "site_key": ""})
		return
	}
	respond(w, 200, map[string]any{"enabled": true, "site_key": s.words.config.Turnstile.SiteKey})
}

type wordSubmissionEntry struct {
	Word   string `json:"word"`
	Pinyin string `json:"pinyin"`
}

type wordSubmissionRequest struct {
	Entries []wordSubmissionEntry `json:"entries"`
	Note    string                `json:"note"`
	Token   string                `json:"token"`
}

type wordRejection struct {
	Index  int    `json:"index"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (s *Server) submitWords(w http.ResponseWriter, r *http.Request) {
	ws := s.words
	if ws == nil {
		wordsFail(w, 503, "word_submissions_disabled", "词条提交暂未开放，请稍后再试。")
		return
	}
	if !slices.Contains(ws.origins, r.Header.Get("Origin")) {
		wordsFail(w, 403, "origin_required", "请从官网表单提交。")
		return
	}
	address := ws.clientAddress(r)
	// A cheap in-memory gate in front of Turnstile so a script cannot make this server call siteverify without bound.
	if !s.allow(Client{ID: "word-submissions:" + address, RequestsPerMinute: 10}, time.Now()) {
		w.Header().Set("Retry-After", "60")
		wordsFail(w, 429, "rate_limit_exceeded", "提交过于频繁，请稍后再试。")
		return
	}
	if ct := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); ct != "application/json" {
		wordsFail(w, 415, "json_required", "请求格式不正确。")
		return
	}
	var input wordSubmissionRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, wordSubmissionBodyBytes))
	d.DisallowUnknownFields()
	err := d.Decode(&input)
	if err == nil && d.Decode(new(any)) != io.EOF {
		err = errors.New("trailing data")
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		wordsFail(w, 413, "request_too_large", "内容过长。")
		return
	}
	if err != nil {
		wordsFail(w, 400, "invalid_json", "请求格式不正确。")
		return
	}
	if len(input.Entries) < 1 || len(input.Entries) > wordSubmissionMaxEntries {
		wordsFail(w, 400, "invalid_entry_count", "每次提交 1 到 20 个词条。")
		return
	}
	note, ok := sanitizeWordNote(input.Note)
	if !ok {
		wordsFail(w, 400, "invalid_note", "备注最多 500 个字。")
		return
	}
	if rejected := validateWordEntries(input.Entries); len(rejected) > 0 {
		respond(w, 400, map[string]any{"error": "部分词条未通过校验，请修改后再提交。", "code": "invalid_entries", "rejected": rejected})
		return
	}
	if input.Token == "" || len(input.Token) > wordSubmissionTokenBytes {
		wordsFail(w, 400, "token_required", "请先完成提交验证。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), wordSubmissionTimeout)
	defer cancel()
	if err = ws.verifyTurnstile(ctx, input.Token); err != nil {
		if errors.Is(err, errTurnstileRejected) {
			wordsFail(w, 403, "verification_failed", "验证已失效，请重新验证后提交。")
		} else {
			slog.Warn("word submissions: turnstile unavailable", "reason", err.Error())
			w.Header().Set("Retry-After", "30")
			wordsFail(w, 503, "verification_unavailable", "验证服务暂时不可用，请重新验证后再试。")
		}
		return
	}
	for _, l := range wordSubmissionLimits {
		if err = ws.limiter.RateLimit(ctx, l.scope, address, l.limit, l.window); err != nil {
			if errors.Is(err, account.ErrLimited) {
				w.Header().Set("Retry-After", l.retryAfter)
				wordsFail(w, 429, "rate_limit_exceeded", "提交过于频繁，请稍后再试。")
			} else {
				slog.Error("word submissions: rate limit unavailable", "reason", err.Error())
				w.Header().Set("Retry-After", "30")
				wordsFail(w, 503, "rate_limit_unavailable", "词条提交暂时不可用，请稍后再试。")
			}
			return
		}
	}
	number, err := ws.submit(ctx, input.Entries, note)
	var listed alreadyListedError
	switch {
	case err == nil:
		respond(w, 201, map[string]string{"pull_request_url": "https://github.com/" + ws.config.GitHub.Repository + "/pull/" + strconv.Itoa(number)})
	case errors.As(err, &listed):
		rejected := make([]wordRejection, 0, len(listed))
		for _, index := range listed {
			rejected = append(rejected, wordRejection{index, "already_listed", "词库中已有这个词条"})
		}
		respond(w, 400, map[string]any{"error": "部分词条已在词库中。", "code": "invalid_entries", "rejected": rejected})
	case errors.Is(err, errWordsConflict):
		wordsFail(w, 409, "concurrent_update", "有其他人同时提交了词条，你的词条尚未写入。请重新验证后再次提交。")
	case errors.Is(err, errWordsUncertain):
		slog.Error("word submissions: GitHub write outcome unknown", "reason", err.Error())
		respond(w, 502, map[string]any{"error": "暂时无法确认提交结果。请先查看词库仓库中最新的 Pull Request，确认词条未写入后再提交。", "code": "outcome_unknown", "uncertain": true, "pulls_url": "https://github.com/" + ws.config.GitHub.Repository + "/pulls"})
	case errors.Is(err, errWordsMisconfigured):
		slog.Error("word submissions: GitHub App misconfigured", "reason", err.Error())
		wordsFail(w, 503, "word_submissions_misconfigured", "词条提交暂未开放，请稍后再试。")
	default:
		slog.Error("word submissions: GitHub unavailable before any write", "reason", err.Error())
		w.Header().Set("Retry-After", "60")
		wordsFail(w, 503, "github_unavailable", "暂时无法读取词库仓库，词条尚未写入，请稍后再试。")
	}
}

// Behind a proxy the TCP peer is the proxy, so the configured header (set by that proxy) wins when it holds an address. IPv6 visitors are limited per /64, the smallest block a single subscriber usually controls.
func (ws *wordSubmitter) clientAddress(r *http.Request) string {
	candidate := ""
	if name := ws.config.ClientIPHeader; name != "" {
		if values := r.Header.Values(name); len(values) > 0 {
			candidate = values[len(values)-1]
			if i := strings.LastIndexByte(candidate, ','); i >= 0 {
				candidate = candidate[i+1:]
			}
		}
	}
	address, err := netip.ParseAddr(strings.TrimSpace(candidate))
	if err != nil {
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			host = r.RemoteAddr
		}
		if address, err = netip.ParseAddr(host); err != nil {
			return host
		}
	}
	address = address.Unmap()
	if address.Is6() {
		prefix, _ := address.Prefix(64)
		return prefix.String()
	}
	return address.String()
}

func wordCharacter(r rune) bool { return unicode.Is(unicode.Unified_Ideograph, r) || r == '〇' }

// One rejection per entry (its first problem), in the same terms the website form uses, so the page can show each next to its row.
func validateWordEntries(entries []wordSubmissionEntry) []wordRejection {
	rejected := []wordRejection{}
	seen := map[string]bool{}
	for i, e := range entries {
		reject := func(code, reason string) { rejected = append(rejected, wordRejection{i, code, reason}) }
		characters := utf8.RuneCountInString(e.Word)
		switch {
		case e.Word == "":
			reject("word_required", "请填写词语")
			continue
		case !utf8.ValidString(e.Word) || strings.IndexFunc(e.Word, func(r rune) bool { return !wordCharacter(r) }) >= 0:
			reject("invalid_word", "词语只能包含汉字，不能有字母、数字、标点或空格")
			continue
		case characters > wordSubmissionMaxChars:
			reject("word_too_long", "词语最多 16 个汉字")
			continue
		}
		if e.Pinyin == "" {
			reject("pinyin_required", "请填写拼音")
			continue
		}
		syllables := strings.Split(e.Pinyin, "'")
		if len(e.Pinyin) > 200 || slices.ContainsFunc(syllables, func(s string) bool {
			return s == "" || strings.IndexFunc(s, func(r rune) bool { return r < 'a' || r > 'z' }) >= 0
		}) {
			reject("invalid_pinyin", "拼音只能包含小写字母，音节之间用 ' 分隔，例如 wei'lai'ke'qi")
			continue
		}
		if bad := slices.IndexFunc(syllables, func(s string) bool { return !pinyinSyllables[s] }); bad >= 0 {
			reject("invalid_syllable", "“"+syllables[bad]+"”不是有效的全拼音节（ü 请写作 v，如 lv、nve）")
			continue
		}
		if len(syllables) != characters {
			reject("syllable_count_mismatch", "“"+e.Word+"”有 "+strconv.Itoa(characters)+" 个字，但拼音有 "+strconv.Itoa(len(syllables))+" 个音节")
			continue
		}
		key := e.Word + "\t" + e.Pinyin
		if seen[key] {
			reject("duplicate_entry", "“"+e.Word+"”重复填写了")
			continue
		}
		seen[key] = true
	}
	return rejected
}

// The note ends up in a public commit message. It is flattened to one line, and every @ (mention), # and GH- (issue references, including closing keywords) and :// (autolinks) is broken with a zero-width space so a submission cannot ping people, touch issues or plant links.
func sanitizeWordNote(note string) (string, bool) {
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > wordSubmissionNoteChars {
		return "", false
	}
	note = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cc, r), r == ' ', r == ' ':
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, note)
	note = strings.Join(strings.Fields(note), " ")
	note = strings.NewReplacer("@", "@​", "#", "#​", "://", ":​//", "GH-", "GH​-", "gh-", "gh​-", "Gh-", "Gh​-", "gH-", "gH​-").Replace(note)
	return note, true
}

var errTurnstileRejected = errors.New("turnstile rejected the token")

func (ws *wordSubmitter) verifyTurnstile(ctx context.Context, token string) error {
	form := url.Values{"secret": {ws.config.Turnstile.secret}, "response": {token}}
	req, err := http.NewRequestWithContext(ctx, "POST", ws.config.Turnstile.SiteverifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ws.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("siteverify returned status " + strconv.Itoa(resp.StatusCode))
	}
	var result struct {
		Success  bool   `json:"success"`
		Action   string `json:"action"`
		Hostname string `json:"hostname"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return errors.New("siteverify returned an invalid response")
	}
	if !result.Success || result.Action != "words" || !slices.Contains(ws.hostnames, result.Hostname) {
		return errTurnstileRejected
	}
	return nil
}

// wordsCommitMessage lists the entries; the note follows on its own line after sanitising.
func wordsCommitMessage(entries []wordSubmissionEntry, note string) string {
	var b strings.Builder
	noun := "words"
	if len(entries) == 1 {
		noun = "word"
	}
	b.WriteString("feat(words): add " + strconv.Itoa(len(entries)) + " community-submitted " + noun + "\n\n")
	for _, e := range entries {
		b.WriteString("- " + e.Word + " " + e.Pinyin + "\n")
	}
	b.WriteString("\nSubmitted anonymously through the MSIME website word form.\n")
	if note != "" {
		b.WriteString("\nNote: " + note + "\n")
	}
	return b.String()
}

// appendWordLines adds word<TAB>pinyin<TAB>weight lines, making sure the existing content ends with a newline first.
func appendWordLines(content string, entries []wordSubmissionEntry) string {
	var b strings.Builder
	b.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	for _, e := range entries {
		b.WriteString(e.Word + "\t" + e.Pinyin + "\t" + strconv.Itoa(wordSubmissionWeight) + "\n")
	}
	return b.String()
}

type alreadyListedError []int

func (alreadyListedError) Error() string { return "entries already listed" }

// listedWords returns the indexes of entries whose word and reading already appear in words.txt.
func listedWords(content string, entries []wordSubmissionEntry) alreadyListedError {
	existing := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) >= 2 {
			existing[fields[0]+"\t"+fields[1]] = true
		}
	}
	var listed alreadyListedError
	for i, e := range entries {
		if existing[e.Word+"\t"+e.Pinyin] {
			listed = append(listed, i)
		}
	}
	return listed
}
