package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
	"github.com/metasequoiaime/MSIME-Backend/internal/githubapp"
)

// Issue triage (unit U3): the issues of admin.github.issue_repos.

const (
	// issueTriagedLabel marks an issue a maintainer has confirmed and assigned to its platform.
	issueTriagedLabel = "triaged"
	// issueDuplicateLabel marks an issue closed as a duplicate of another.
	issueDuplicateLabel = "duplicate"
	// issueOpenPages bounds how many pages of 100 open issues are read per repository.
	issueOpenPages = 3
	// issuePageSize is the page size of GET /api/issues.
	issuePageSize = 50
	// issueMaxItems bounds the items of one POST /api/issues/actions request, like the ids of /api/actions.
	issueMaxItems = 100
	// issueResponseWindow is how far back the average first response time looks.
	issueResponseWindow = 30 * 24 * time.Hour
	// issueFetchParallel bounds the repositories read at the same time.
	issueFetchParallel = 4
)

// issueTokenPerms is the scope of every installation token minted here: reading and writing the issues of one repository.
var issueTokenPerms = map[string]string{"issues": "write", "metadata": "read"}

// issueActions are the actions of POST /api/issues/actions. Each has a reverse for undo (triage and untriage, mark_dup or close and reopen) except comment.
var issueActions = []string{"triage", "untriage", "mark_dup", "close", "reopen", "comment"}

// issueStates are the state filters GET /api/issues accepts: the four console states plus open (new and triaged), closed (done and dup) and all.
var issueStates = []string{"all", "open", "new", "triaged", "closed", "done", "dup"}

// issueGitHubUser is the part of a GitHub user object the console needs.
type issueGitHubUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type issueLabel struct {
	Name string `json:"name"`
}

// issueGitHub is the part of a GitHub REST issue the console needs.
type issueGitHub struct {
	Number      int               `json:"number"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	State       string            `json:"state"`
	StateReason string            `json:"state_reason"`
	HTMLURL     string            `json:"html_url"`
	User        issueGitHubUser   `json:"user"`
	Labels      []issueLabel      `json:"labels"`
	Assignees   []issueGitHubUser `json:"assignees"`
	Comments    int               `json:"comments"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	ClosedAt    *time.Time        `json:"closed_at"`
	PullRequest json.RawMessage   `json:"pull_request"`
}

func (v issueGitHub) isPullRequest() bool {
	return len(v.PullRequest) != 0 && string(v.PullRequest) != "null"
}

// issueRow is one issue as the console lists it.
type issueRow struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url"`
	// State is new (open, not triaged), triaged (open, triaged), done (closed) or dup (closed as a duplicate).
	State string `json:"state"`
	// Platform is the id of the admin.github.platforms entry the issue belongs to, or "" when none matches.
	Platform string `json:"platform"`
	// Kind is bug, idea or docs from the issue's labels, or "" when no label says.
	Kind      string     `json:"kind"`
	Labels    []string   `json:"labels"`
	Assignees []string   `json:"assignees"`
	Comments  int        `json:"comments"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
}

// issueRef names one issue in an action request.
type issueRef struct {
	Repo string `json:"repo"`
	N    int    `json:"n"`
}

func (ref issueRef) key() string { return ref.Repo + "#" + strconv.Itoa(ref.N) }

// issueSnapshot is one read of every configured repository.
type issueSnapshot struct {
	items []issueRow
	// unavailable lists the repositories that could not be read.
	unavailable []string
	// responseHours is the average time to the first maintainer comment on issues opened in the last 30 days, over samples issues; nil without samples.
	responseHours *float64
	samples       int
}

// issueMemory is what the issue triage keeps between requests for one Server: the last listed issues for the global search, and which issues it has already announced.
type issueMemory struct {
	mu    sync.Mutex
	items []issueRow
	// baseline is when the first complete snapshot was taken; only issues created after it are announced, so a restart does not announce the backlog.
	baseline time.Time
	seen     map[string]bool
}

// issueMemories holds the issueMemory of each Server; the Server struct is shared with other units, so the state lives here instead of in a field.
var issueMemories sync.Map

func (s *Server) issueMemory() *issueMemory {
	m, _ := issueMemories.LoadOrStore(s, &issueMemory{})
	return m.(*issueMemory)
}

// issueError is a failure answered with status and code.
type issueError struct {
	status int
	code   string
}

func (e *issueError) Error() string { return e.code }

func issueFail(w http.ResponseWriter, err error) {
	var e *issueError
	if errors.As(err, &e) {
		fail(w, e.status, e.code)
		return
	}
	slog.Error("issues: unexpected failure", "reason", err.Error())
	fail(w, 502, "github_unavailable")
}

// issueTokenError classifies a failure to mint an installation token.
func issueTokenError(repo string, err error) error {
	slog.Error("issues: GitHub installation token", "repo", repo, "reason", err.Error())
	if errors.Is(err, githubapp.ErrRejected) {
		return &issueError{502, "github_rejected"}
	}
	return &issueError{502, "github_unavailable"}
}

// issueResponseError classifies a GitHub REST answer that is not a success. A missing issue is 404 not_found, any other refusal means the App lacks access (github_rejected), and server errors, transport failures and unreadable bodies mean GitHub is unavailable.
func issueResponseError(what string, r githubapp.Response, err error) error {
	if err != nil || r.Status >= 500 || r.OK() {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		slog.Error("issues: GitHub unavailable", "call", what, "status", r.Status, "reason", reason)
		return &issueError{502, "github_unavailable"}
	}
	if r.Status == 404 || r.Status == 410 {
		return &issueError{404, "not_found"}
	}
	slog.Error("issues: GitHub refused", "call", what, "status", r.Status)
	return &issueError{502, "github_rejected"}
}

// adminIssues serves every path under /api/issues: GET /api/issues, GET /api/issues/{owner}/{repo}/{n} and POST /api/issues/actions.
func (s *Server) adminIssues(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/issues":
		if r.Method != "GET" {
			fail(w, 405, "method_not_allowed")
			return
		}
		s.listIssues(w, r)
	case r.URL.Path == "/api/issues/actions":
		if r.Method != "POST" {
			fail(w, 405, "method_not_allowed")
			return
		}
		s.issueActionsHTTP(w, r)
	default:
		if r.Method != "GET" {
			fail(w, 405, "method_not_allowed")
			return
		}
		s.issueDetail(w, r, strings.TrimPrefix(r.URL.Path, "/api/issues/"))
	}
}

// issueRepo returns the configured spelling of repo, or false when it is not one of admin.github.issue_repos.
func (s *Server) issueRepo(repo string) (string, bool) {
	for _, configured := range s.config.Admin.GitHub.IssueRepos {
		if strings.EqualFold(configured, repo) {
			return configured, true
		}
	}
	return "", false
}

// issuePlatform maps an issue to the platform whose label it carries; without such a label, an issue in a repository that belongs to exactly one platform is that platform's.
func (s *Server) issuePlatform(repo string, labels []string) *AdminPlatformConfig {
	platforms := s.config.Admin.GitHub.Platforms
	for i := range platforms {
		if platforms[i].Label != "" && issueHasLabel(labels, platforms[i].Label) {
			return &platforms[i]
		}
	}
	var match *AdminPlatformConfig
	for i := range platforms {
		if strings.EqualFold(platforms[i].Repo, repo) {
			if match != nil {
				return nil
			}
			match = &platforms[i]
		}
	}
	return match
}

func issueHasLabel(labels []string, name string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, name) })
}

// issueKind reads the issue type from the conventional GitHub labels.
func issueKind(labels []string) string {
	for _, l := range labels {
		switch strings.ToLower(l) {
		case "bug", "问题", "type: bug", "kind/bug":
			return "bug"
		case "enhancement", "feature", "feature request", "idea", "suggestion", "建议", "type: feature", "kind/feature":
			return "idea"
		case "documentation", "docs", "文档", "type: docs", "kind/docs":
			return "docs"
		}
	}
	return ""
}

// issueState maps GitHub's state and labels to the four console states.
func issueState(state, reason string, labels []string) string {
	if state == "closed" {
		if reason == "duplicate" || issueHasLabel(labels, issueDuplicateLabel) {
			return "dup"
		}
		return "done"
	}
	if issueHasLabel(labels, issueTriagedLabel) {
		return "triaged"
	}
	return "new"
}

func (s *Server) issueRow(repo string, v issueGitHub) issueRow {
	labels := make([]string, 0, len(v.Labels))
	for _, l := range v.Labels {
		labels = append(labels, l.Name)
	}
	assignees := make([]string, 0, len(v.Assignees))
	for _, a := range v.Assignees {
		assignees = append(assignees, a.Login)
	}
	platform := ""
	if p := s.issuePlatform(repo, labels); p != nil {
		platform = p.ID
	}
	return issueRow{
		Repo: repo, Number: v.Number, Title: v.Title, Author: v.User.Login, URL: v.HTMLURL,
		State: issueState(v.State, v.StateReason, labels), Platform: platform, Kind: issueKind(labels),
		Labels: labels, Assignees: assignees, Comments: v.Comments,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, ClosedAt: v.ClosedAt,
	}
}

// issueComment is the part of a repository issue comment needed for response times.
type issueComment struct {
	IssueURL          string          `json:"issue_url"`
	User              issueGitHubUser `json:"user"`
	AuthorAssociation string          `json:"author_association"`
	CreatedAt         time.Time       `json:"created_at"`
}

// repoIssues reads one repository: up to issueOpenPages pages of open issues, the 100 most recently updated closed issues, and the 100 newest comments for response times. Pull requests are dropped. Every read goes through the client's short cache, so the shell badge polling this costs GitHub at most one revalidation per path a minute.
func (s *Server) repoIssues(ctx context.Context, gh *githubapp.Client, repo string) ([]issueRow, []issueComment, error) {
	token, err := gh.Token(ctx, repo, issueTokenPerms)
	if err != nil {
		return nil, nil, issueTokenError(repo, err)
	}
	var rows []issueRow
	read := func(path string) (int, error) {
		r, err := gh.Get(ctx, token, path)
		if err != nil || !r.OK() {
			return 0, issueResponseError("list issues", r, err)
		}
		var page []issueGitHub
		if err := r.Decode(&page); err != nil {
			return 0, issueResponseError("list issues", r, err)
		}
		for _, v := range page {
			if !v.isPullRequest() {
				rows = append(rows, s.issueRow(repo, v))
			}
		}
		return len(page), nil
	}
	base := "/repos/" + repo + "/issues"
	for page := 1; page <= issueOpenPages; page++ {
		n, err := read(base + "?state=open&sort=created&direction=desc&per_page=100&page=" + strconv.Itoa(page))
		if err != nil {
			return nil, nil, err
		}
		if n < 100 {
			break
		}
	}
	if _, err := read(base + "?state=closed&sort=updated&direction=desc&per_page=100"); err != nil {
		return nil, nil, err
	}
	r, err := gh.Get(ctx, token, base+"/comments?sort=created&direction=desc&per_page=100")
	if err != nil || !r.OK() {
		return nil, nil, issueResponseError("list comments", r, err)
	}
	var comments []issueComment
	if err := r.Decode(&comments); err != nil {
		return nil, nil, issueResponseError("list comments", r, err)
	}
	return rows, comments, nil
}

// issueSnapshot reads every configured repository in parallel. A repository that fails is listed in unavailable; the snapshot fails only when every repository failed. The result also refreshes the search index and announces issues created since the first snapshot.
func (s *Server) issueSnapshot(ctx context.Context, gh *githubapp.Client) (issueSnapshot, error) {
	repos := s.config.Admin.GitHub.IssueRepos
	type result struct {
		rows     []issueRow
		comments []issueComment
		err      error
	}
	results := make([]result, len(repos))
	slots := make(chan struct{}, issueFetchParallel)
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			rows, comments, err := s.repoIssues(ctx, gh, repo)
			results[i] = result{rows, comments, err}
		}()
	}
	wg.Wait()
	var snap issueSnapshot
	var firstErr error
	now := time.Now()
	var totalHours float64
	for i, res := range results {
		if res.err != nil {
			snap.unavailable = append(snap.unavailable, repos[i])
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		snap.items = append(snap.items, res.rows...)
		hours, samples := issueFirstResponses(res.rows, res.comments, now)
		totalHours += hours
		snap.samples += samples
	}
	if len(repos) > 0 && len(snap.unavailable) == len(repos) {
		return issueSnapshot{}, firstErr
	}
	if snap.samples > 0 {
		avg := totalHours / float64(snap.samples)
		snap.responseHours = &avg
	}
	slices.SortStableFunc(snap.items, func(a, b issueRow) int { return b.CreatedAt.Compare(a.CreatedAt) })
	s.rememberIssues(ctx, snap.items, len(snap.unavailable) == 0, now)
	return snap, nil
}

// issueFirstResponses sums, over the issues of one repository opened within issueResponseWindow, the hours until the first comment by a maintainer (owner, member or collaborator) who is neither the author nor a bot, and returns that sum with the number of issues it covers.
func issueFirstResponses(rows []issueRow, comments []issueComment, now time.Time) (float64, int) {
	authors := map[int]string{}
	for _, row := range rows {
		if now.Sub(row.CreatedAt) <= issueResponseWindow {
			authors[row.Number] = row.Author
		}
	}
	first := map[int]time.Time{}
	for _, c := range comments {
		idx := strings.LastIndex(c.IssueURL, "/issues/")
		if idx < 0 {
			continue
		}
		n, err := strconv.Atoi(c.IssueURL[idx+len("/issues/"):])
		author, tracked := authors[n]
		if err != nil || !tracked || strings.EqualFold(c.User.Login, author) || c.User.Type == "Bot" {
			continue
		}
		switch c.AuthorAssociation {
		case "OWNER", "MEMBER", "COLLABORATOR":
		default:
			continue
		}
		if at, ok := first[n]; !ok || c.CreatedAt.Before(at) {
			first[n] = c.CreatedAt
		}
	}
	var hours float64
	samples := 0
	for _, row := range rows {
		at, ok := first[row.Number]
		if _, tracked := authors[row.Number]; !ok || !tracked || at.Before(row.CreatedAt) {
			continue
		}
		hours += at.Sub(row.CreatedAt).Hours()
		samples++
	}
	return hours, samples
}

// rememberIssues keeps the listed issues for the global search and announces each untriaged issue created since the first snapshot once. The first snapshot only sets the baseline; complete is false when a repository failed, so the baseline waits for a full read.
func (s *Server) rememberIssues(ctx context.Context, items []issueRow, complete bool, now time.Time) {
	m := s.issueMemory()
	m.mu.Lock()
	m.items = items
	var announce []issueRow
	if m.baseline.IsZero() {
		if complete {
			m.baseline = now
			m.seen = map[string]bool{}
		}
	} else {
		for _, row := range items {
			key := issueRef{row.Repo, row.Number}.key()
			if row.State == "new" && row.CreatedAt.After(m.baseline) && !m.seen[key] {
				m.seen[key] = true
				announce = append(announce, row)
			}
		}
	}
	m.mu.Unlock()
	for _, row := range announce {
		title := "#" + strconv.Itoa(row.Number) + " " + row.Title
		if utf8.RuneCountInString(title) > 300 {
			title = string([]rune(title)[:299]) + "…"
		}
		key := issueRef{row.Repo, row.Number}.key()
		if err := s.accounts.NotifyNow(ctx, account.Notification{Kind: account.NotifyIssue, Title: title, TargetPage: "issues", TargetID: key}); err != nil {
			slog.Error("issues: notification failed", "issue", key, "reason", err.Error())
		}
	}
}

// issuePlatformView is a configured platform as the issues page shows it.
type issuePlatformView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"`
	// Assignee is who triage assigns this platform's issues to, "" for nobody.
	Assignee string `json:"assignee"`
}

// listIssues serves GET /api/issues?platform=&state=&page=. platform is all (the default), a platform id or other (no platform); state is one of issueStates and defaults to open. The answer carries one page of matching issues, the per-platform counts under the state filter (for the filter chips), the per-state counts under the platform filter, and the stat tiles over every listed issue.
func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	gh, ok := s.adminGitHubApp(w)
	if !ok {
		return
	}
	query := r.URL.Query()
	platform := query.Get("platform")
	if platform == "" {
		platform = "all"
	}
	if platform != "all" && platform != "other" && !slices.ContainsFunc(s.config.Admin.GitHub.Platforms, func(p AdminPlatformConfig) bool { return p.ID == platform }) {
		fail(w, 400, "invalid_platform")
		return
	}
	state := query.Get("state")
	if state == "" {
		state = "open"
	}
	if !slices.Contains(issueStates, state) {
		fail(w, 400, "invalid_state")
		return
	}
	page := 1
	if raw := query.Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 || strconv.Itoa(n) != raw {
			fail(w, 400, "invalid_page")
			return
		}
		page = n
	}
	snap, err := s.issueSnapshot(r.Context(), gh)
	if err != nil {
		issueFail(w, err)
		return
	}
	platforms := make([]issuePlatformView, 0, len(s.config.Admin.GitHub.Platforms))
	platformCounts := map[string]int{"all": 0, "other": 0}
	for _, p := range s.config.Admin.GitHub.Platforms {
		platforms = append(platforms, issuePlatformView{p.ID, p.Name, p.Label, p.Assignee})
		platformCounts[p.ID] = 0
	}
	stateCounts := map[string]int{"new": 0, "triaged": 0, "done": 0, "dup": 0}
	items := []issueRow{}
	weekAgo := time.Now().Add(-7 * 24 * time.Hour)
	pending, triaged, week := 0, 0, 0
	for _, row := range snap.items {
		switch row.State {
		case "new":
			pending++
		case "triaged":
			triaged++
		}
		if row.CreatedAt.After(weekAgo) {
			week++
		}
		rowPlatform := row.Platform
		if rowPlatform == "" {
			rowPlatform = "other"
		}
		if issueStateMatches(state, row.State) {
			platformCounts["all"]++
			platformCounts[rowPlatform]++
		}
		if platform == "all" || platform == rowPlatform {
			stateCounts[row.State]++
			if issueStateMatches(state, row.State) {
				items = append(items, row)
			}
		}
	}
	total := len(items)
	start := min((page-1)*issuePageSize, total)
	end := min(start+issuePageSize, total)
	repos := s.config.Admin.GitHub.IssueRepos
	if repos == nil {
		repos = []string{}
	}
	unavailable := snap.unavailable
	if unavailable == nil {
		unavailable = []string{}
	}
	respond(w, 200, map[string]any{
		"items": items[start:end], "page": page, "page_size": issuePageSize, "total": total, "has_more": end < total,
		"repos": repos, "platforms": platforms, "unavailable": unavailable,
		"platform_counts": platformCounts, "state_counts": stateCounts,
		"stats": map[string]any{"pending": pending, "triaged": triaged, "new_this_week": week, "first_response_hours": snap.responseHours, "first_response_samples": snap.samples},
	})
}

func issueStateMatches(filter, state string) bool {
	switch filter {
	case "all":
		return true
	case "open":
		return state == "new" || state == "triaged"
	case "closed":
		return state == "done" || state == "dup"
	}
	return filter == state
}

// parseIssuePath parses "{owner}/{repo}/{n}" to a configured repository and a positive issue number; any other repository is not found.
func (s *Server) parseIssuePath(rest string) (issueRef, error) {
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return issueRef{}, &issueError{404, "not_found"}
	}
	repo, ok := s.issueRepo(parts[0] + "/" + parts[1])
	if !ok {
		return issueRef{}, &issueError{404, "not_found"}
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 1 || strconv.Itoa(n) != parts[2] {
		return issueRef{}, &issueError{400, "invalid_id"}
	}
	return issueRef{repo, n}, nil
}

// issueEvent is one entry of an issue's timeline.
type issueEvent struct {
	// Kind is created, commented, labeled, unlabeled, assigned, unassigned, closed, reopened, renamed or marked_as_duplicate.
	Kind  string `json:"kind"`
	Actor string `json:"actor"`
	// Text is the comment body, the label, the assignee, the new title or the close reason, by kind.
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// issueTimelineGitHub is the part of a GitHub timeline event the console shows.
type issueTimelineGitHub struct {
	Event     string           `json:"event"`
	Actor     *issueGitHubUser `json:"actor"`
	User      *issueGitHubUser `json:"user"`
	Body      string           `json:"body"`
	Label     *issueLabel      `json:"label"`
	Assignee  *issueGitHubUser `json:"assignee"`
	CreatedAt time.Time        `json:"created_at"`
	Rename    *struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rename"`
	StateReason string `json:"state_reason"`
}

func (e issueTimelineGitHub) actor() string {
	if e.Actor != nil {
		return e.Actor.Login
	}
	if e.User != nil {
		return e.User.Login
	}
	return ""
}

// issueSimilar is a listed issue whose title resembles another's.
type issueSimilar struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
}

// issueDetail serves GET /api/issues/{owner}/{repo}/{n}: the issue with its body, its timeline (comments, labels, assignments, closing and reopening), the assignee triage would pick and up to three listed issues with similar titles.
func (s *Server) issueDetail(w http.ResponseWriter, r *http.Request, rest string) {
	gh, ok := s.adminGitHubApp(w)
	if !ok {
		return
	}
	ref, err := s.parseIssuePath(rest)
	if err != nil {
		issueFail(w, err)
		return
	}
	ctx := r.Context()
	token, err := gh.Token(ctx, ref.Repo, issueTokenPerms)
	if err != nil {
		issueFail(w, issueTokenError(ref.Repo, err))
		return
	}
	issue, err := issueFetch(ctx, gh, token, ref, true)
	if err != nil {
		issueFail(w, err)
		return
	}
	resp, err := gh.Get(ctx, token, fmt.Sprintf("/repos/%s/issues/%d/timeline?per_page=100", ref.Repo, ref.N))
	if err != nil || !resp.OK() {
		issueFail(w, issueResponseError("timeline", resp, err))
		return
	}
	var events []issueTimelineGitHub
	if err := resp.Decode(&events); err != nil {
		issueFail(w, issueResponseError("timeline", resp, err))
		return
	}
	timeline := []issueEvent{{Kind: "created", Actor: issue.User.Login, At: issue.CreatedAt}}
	for _, e := range events {
		event := issueEvent{Kind: e.Event, Actor: e.actor(), At: e.CreatedAt}
		switch e.Event {
		case "commented":
			event.Text = e.Body
		case "labeled", "unlabeled":
			if e.Label == nil {
				continue
			}
			event.Text = e.Label.Name
		case "assigned", "unassigned":
			if e.Assignee == nil {
				continue
			}
			event.Text = e.Assignee.Login
		case "renamed":
			if e.Rename == nil {
				continue
			}
			event.Text = e.Rename.To
		case "closed":
			event.Text = e.StateReason
		case "reopened", "marked_as_duplicate":
		default:
			continue
		}
		timeline = append(timeline, event)
	}
	row := s.issueRow(ref.Repo, issue)
	assignee := ""
	if p := s.issuePlatform(ref.Repo, row.Labels); p != nil {
		assignee = p.Assignee
	}
	respond(w, 200, map[string]any{
		"issue": struct {
			issueRow
			Body string `json:"body"`
		}{row, issue.Body},
		"timeline": timeline, "similar": s.similarIssues(ctx, gh, row), "platform_assignee": assignee,
	})
}

// similarIssues returns up to three listed issues whose titles share the most character bigrams with row's title. Bigrams work for Chinese titles, which have no spaces for a word search to split on, and the listing is already cached, so this spends none of GitHub's search quota. A listing failure only leaves the list empty.
func (s *Server) similarIssues(ctx context.Context, gh *githubapp.Client, row issueRow) []issueSimilar {
	out := []issueSimilar{}
	snap, err := s.issueSnapshot(ctx, gh)
	if err != nil {
		return out
	}
	target := titleBigrams(row.Title)
	type scored struct {
		row   issueRow
		score float64
	}
	var candidates []scored
	for _, other := range snap.items {
		if other.Repo == row.Repo && other.Number == row.Number {
			continue
		}
		if score := bigramSimilarity(target, titleBigrams(other.Title)); score >= 0.3 {
			candidates = append(candidates, scored{other, score})
		}
	}
	slices.SortStableFunc(candidates, func(a, b scored) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return b.row.CreatedAt.Compare(a.row.CreatedAt)
	})
	for _, c := range candidates[:min(3, len(candidates))] {
		out = append(out, issueSimilar{c.row.Repo, c.row.Number, c.row.Title, c.row.State, c.row.URL})
	}
	return out
}

// titleBigrams is the set of adjacent letter or digit pairs of a lowercased title; punctuation and spaces separate words and never join a pair.
func titleBigrams(title string) map[string]bool {
	set := map[string]bool{}
	var prev rune
	for _, c := range strings.ToLower(title) {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) {
			prev = 0
			continue
		}
		if prev != 0 {
			set[string([]rune{prev, c})] = true
		}
		prev = c
	}
	return set
}

// bigramSimilarity is the Dice coefficient of two bigram sets.
func bigramSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for k := range a {
		if b[k] {
			shared++
		}
	}
	return 2 * float64(shared) / float64(len(a)+len(b))
}

// issueFetch reads one issue, from the short read cache when cached is true and fresh otherwise. A pull request counts as not found.
func issueFetch(ctx context.Context, gh *githubapp.Client, token string, ref issueRef, cached bool) (issueGitHub, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d", ref.Repo, ref.N)
	var resp githubapp.Response
	var err error
	if cached {
		resp, err = gh.Get(ctx, token, path)
	} else {
		resp, err = gh.Do(ctx, token, "GET", path, nil)
	}
	if err != nil || !resp.OK() {
		return issueGitHub{}, issueResponseError("get issue", resp, err)
	}
	var issue issueGitHub
	if err := resp.Decode(&issue); err != nil || issue.Number != ref.N {
		return issueGitHub{}, issueResponseError("get issue", resp, err)
	}
	if issue.isPullRequest() {
		return issueGitHub{}, &issueError{404, "not_found"}
	}
	return issue, nil
}

// issueActionRequest is the body of POST /api/issues/actions.
type issueActionRequest struct {
	Action string     `json:"action"`
	Items  []issueRef `json:"items"`
	Body   string     `json:"body"`
}

// issueActionFailure is an item that could not be changed.
type issueActionFailure struct {
	Repo string `json:"repo"`
	N    int    `json:"n"`
	Code string `json:"code"`
}

// issueActionsHTTP serves POST /api/issues/actions {action, items:[{repo,n}], body?}. GitHub has no transactions, so each item is changed on its own and audited right after GitHub accepted it; the answer reports how many items changed and which failed. When no item changed, the first failure is the answer.
func (s *Server) issueActionsHTTP(w http.ResponseWriter, r *http.Request) {
	gh, ok := s.adminGitHubApp(w)
	if !ok {
		return
	}
	if !requirePerm(w, r, account.PermTriageIssues) {
		return
	}
	var v issueActionRequest
	if !decode(w, r, &v) {
		return
	}
	if !slices.Contains(issueActions, v.Action) {
		fail(w, 400, "invalid_action")
		return
	}
	if len(v.Items) == 0 || len(v.Items) > issueMaxItems {
		fail(w, 400, "invalid_items")
		return
	}
	seen := map[string]bool{}
	for i, item := range v.Items {
		repo, ok := s.issueRepo(item.Repo)
		ref := issueRef{repo, item.N}
		if !ok || item.N < 1 || seen[ref.key()] {
			fail(w, 400, "invalid_items")
			return
		}
		seen[ref.key()] = true
		v.Items[i] = ref
	}
	// The 64 KiB request body limit of decode keeps a comment under GitHub's 65536 character limit.
	if v.Action == "comment" {
		if strings.TrimSpace(v.Body) == "" || strings.ContainsRune(v.Body, 0) {
			fail(w, 400, "invalid_body")
			return
		}
	} else if v.Body != "" {
		fail(w, 400, "invalid_body")
		return
	}
	ctx := r.Context()
	affected := 0
	failures := []issueActionFailure{}
	var firstErr error
	for _, item := range v.Items {
		detail, err := s.issueAction(ctx, gh, v.Action, item, v.Body)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			code := "github_unavailable"
			var e *issueError
			if errors.As(err, &e) {
				code = e.code
			}
			failures = append(failures, issueActionFailure{item.Repo, item.N, code})
			continue
		}
		if err := s.accounts.Audit(ctx, "issue_"+v.Action, item.key(), detail); err != nil {
			// GitHub already applied this item; stop before changing more without a record, and report the request as failed.
			slog.Error("issues: audit failed after a GitHub write", "action", v.Action, "issue", item.key(), "reason", err.Error())
			fail(w, 503, "auth_unavailable")
			return
		}
		affected++
	}
	if affected == 0 {
		issueFail(w, firstErr)
		return
	}
	respond(w, 200, map[string]any{"ok": true, "affected": affected, "failed": failures})
}

// issueAction applies one action to one issue and returns the audit detail. Every write first reads the issue fresh, so a pull request or a missing issue fails as not_found before anything changes and the audit records the state the action started from.
func (s *Server) issueAction(ctx context.Context, gh *githubapp.Client, action string, ref issueRef, body string) (map[string]any, error) {
	token, err := gh.Token(ctx, ref.Repo, issueTokenPerms)
	if err != nil {
		return nil, issueTokenError(ref.Repo, err)
	}
	issue, err := issueFetch(ctx, gh, token, ref, false)
	if err != nil {
		return nil, err
	}
	// Whatever part of the action reached GitHub, the cached listing and detail of this repository are stale now.
	defer gh.Invalidate("/repos/" + ref.Repo + "/issues")
	row := s.issueRow(ref.Repo, issue)
	detail := map[string]any{"repo": ref.Repo, "number": ref.N, "title": issue.Title, "from": row.State}
	base := fmt.Sprintf("/repos/%s/issues/%d", ref.Repo, ref.N)
	// call performs one write; allowMissing accepts 404, which removing a label the issue no longer has answers.
	call := func(what, method, path string, payload any, allowMissing bool) error {
		resp, err := gh.Do(ctx, token, method, path, payload)
		if err == nil && (resp.OK() || allowMissing && resp.Status == 404) {
			return nil
		}
		return issueResponseError(what, resp, err)
	}
	platform := s.issuePlatform(ref.Repo, row.Labels)
	switch action {
	case "triage":
		if err := call("add label", "POST", base+"/labels", map[string][]string{"labels": {issueTriagedLabel}}, false); err != nil {
			return nil, err
		}
		if platform != nil {
			detail["platform"] = platform.ID
			if platform.Assignee != "" {
				if err := call("assign", "POST", base+"/assignees", map[string][]string{"assignees": {platform.Assignee}}, false); err != nil {
					return nil, err
				}
				detail["assignee"] = platform.Assignee
			}
		}
		detail["to"] = "triaged"
	case "untriage":
		if err := call("remove label", "DELETE", base+"/labels/"+url.PathEscape(issueTriagedLabel), nil, true); err != nil {
			return nil, err
		}
		if platform != nil && platform.Assignee != "" && slices.ContainsFunc(row.Assignees, func(a string) bool { return strings.EqualFold(a, platform.Assignee) }) {
			if err := call("unassign", "DELETE", base+"/assignees", map[string][]string{"assignees": {platform.Assignee}}, false); err != nil {
				return nil, err
			}
			detail["assignee"] = platform.Assignee
		}
		detail["to"] = "new"
	case "mark_dup":
		if err := call("add label", "POST", base+"/labels", map[string][]string{"labels": {issueDuplicateLabel}}, false); err != nil {
			return nil, err
		}
		if err := call("close", "PATCH", base, map[string]string{"state": "closed", "state_reason": "not_planned"}, false); err != nil {
			return nil, err
		}
		detail["to"] = "dup"
	case "close":
		if err := call("close", "PATCH", base, map[string]string{"state": "closed", "state_reason": "completed"}, false); err != nil {
			return nil, err
		}
		detail["to"] = "done"
	case "reopen":
		if err := call("reopen", "PATCH", base, map[string]string{"state": "open", "state_reason": "reopened"}, false); err != nil {
			return nil, err
		}
		if issueHasLabel(row.Labels, issueDuplicateLabel) {
			if err := call("remove label", "DELETE", base+"/labels/"+url.PathEscape(issueDuplicateLabel), nil, true); err != nil {
				return nil, err
			}
		}
		detail["to"] = "new"
		if issueHasLabel(row.Labels, issueTriagedLabel) {
			detail["to"] = "triaged"
		}
	case "comment":
		if err := call("comment", "POST", base+"/comments", map[string]string{"body": body}, false); err != nil {
			return nil, err
		}
		delete(detail, "from")
		detail["length"] = utf8.RuneCountInString(body)
	}
	return detail, nil
}

// searchIssues matches q against the cached issues for the global search; it never calls GitHub.
func (s *Server) searchIssues(q string) []account.AdminSearchHit {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	number := strings.TrimPrefix(q, "#")
	m := s.issueMemory()
	m.mu.Lock()
	items := m.items
	m.mu.Unlock()
	var hits []account.AdminSearchHit
	for _, row := range items {
		n := strconv.Itoa(row.Number)
		if number != n && !strings.Contains(strings.ToLower(row.Title), q) && !strings.Contains(strings.ToLower(row.Repo+"#"+n), q) {
			continue
		}
		hits = append(hits, account.AdminSearchHit{Kind: "issue", ID: issueRef{row.Repo, row.Number}.key(), Title: "#" + n + " " + row.Title, Where: "问题分诊", Target: "issues"})
		if len(hits) == 8 {
			break
		}
	}
	return hits
}

// pendingIssues counts open, untriaged issues for the shell badge.
func (s *Server) pendingIssues(ctx context.Context) (int, error) {
	if s.adminGitHub == nil || len(s.config.Admin.GitHub.IssueRepos) == 0 {
		return 0, nil
	}
	snap, err := s.issueSnapshot(ctx, s.adminGitHub)
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, row := range snap.items {
		if row.State == "new" {
			pending++
		}
	}
	return pending, nil
}
