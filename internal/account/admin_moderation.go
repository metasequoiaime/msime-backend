package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Community moderation page (unit U2): the five content lists, deletion, approve/remove/restore and the pending counts.

// Moderation is post-moderation: uploads are public at once with moderation='pending', and only 'removed' rows are hidden from everyone but their owner. For a pending row, moderation_reason holds the automatic sensitive-word flag; for a removed row it holds the moderator's reason.

// moderationColumns are the moderation fields every list row carries; flag is the automatic check's warning, shown only while the row awaits review. alias and section are fixed identifiers, never request text.
func moderationColumns(alias, section string) string {
	return alias + `.moderation,` + alias + `.moderation_reason,` + alias + `.moderated_by,` + alias + `.moderated_at,
 CASE WHEN ` + alias + `.moderation='pending' THEN ` + alias + `.moderation_reason END AS flag,
 (SELECT count(*) FROM community_reports WHERE kind='` + section + `' AND item_id=` + alias + `.id) AS reports`
}

// statusFilter selects rows by moderation state.
var statusFilter = listFilter{param: "status", field: "moderation", max: 8, values: map[string]string{"pending": "pending", "approved": "approved", "removed": "removed"}}

const authorColumn = `COALESCE(NULLIF(btrim(u.display_name),''),'水杉小鹿·'||upper(left(u.id,6))) AS author`

// The lists serve GET /api/{skins,candidate-skins,plugins,dictionaries,replies}.
var (
	// The skin design travels without its photo, so the console can draw the keyboard preview on each card.
	skinsList = adminList{
		query:   `SELECT s.id,s.name,s.description,s.owner_id,` + authorColumn + `,s.created_at,s.design-'photo' AS design,(SELECT count(*) FROM community_skin_downloads WHERE skin_id=s.id) AS downloads,` + moderationColumns("s", "skins") + ` FROM community_skins s JOIN auth_users u ON u.id=s.owner_id`,
		filters: []listFilter{statusFilter},
	}
	// Candidate skins hold both the public gallery and each account's private library, so moderation can list either one.
	candidateSkinsList = adminList{
		query:   `SELECT s.id,s.package_id,s.name,s.description,s.owner_id,` + authorColumn + `,s.version,(SELECT COALESCE(sum(size),0) FROM community_candidate_skin_files WHERE skin_id=s.id) AS size,(SELECT count(*) FROM community_candidate_skin_files WHERE skin_id=s.id) AS file_count,(SELECT count(*) FROM community_candidate_skin_downloads WHERE skin_id=s.id) AS downloads,s.visibility,s.created_at,s.updated_at,` + moderationColumns("s", "candidate-skins") + ` FROM community_candidate_skins s JOIN auth_users u ON u.id=s.owner_id`,
		filters: []listFilter{{param: "visibility", field: "visibility", max: 7, values: map[string]string{"public": "public", "private": "private"}}, statusFilter},
	}
	pluginsList = adminList{
		query:   `SELECT p.id,p.kind,p.plugin_id,p.name,p.description,p.version,` + authorColumn + `,p.owner_id,p.size,p.sha256,(SELECT count(*) FROM community_plugin_downloads WHERE pack_id=p.id) AS downloads,p.created_at,` + moderationColumns("p", "plugins") + ` FROM community_plugins p JOIN auth_users u ON u.id=p.owner_id`,
		filters: []listFilter{statusFilter},
	}
	// preview is the first three entries, for the card's content lines.
	dictionariesList = adminList{
		query:   `SELECT r.id,r.name,r.description,r.owner_id,` + authorColumn + `,r.revision,r.created_at,r.updated_at,jsonb_array_length(r.content->'entries') AS entries,(SELECT jsonb_agg(e) FROM (SELECT e FROM jsonb_array_elements(r.content->'entries') e LIMIT 3) x) AS preview,(SELECT count(*) FROM community_resource_saves WHERE resource_id=r.id) AS saves,` + moderationColumns("r", "dictionaries") + ` FROM community_resources r JOIN auth_users u ON u.id=r.owner_id WHERE r.kind='dictionary'`,
		filters: []listFilter{statusFilter},
	}
	repliesList = adminList{
		query:   `SELECT r.id,r.name,r.description,r.owner_id,` + authorColumn + `,r.revision,r.created_at,r.updated_at,r.content->>'prompt' AS prompt,(SELECT count(*) FROM community_resource_saves WHERE resource_id=r.id) AS saves,` + moderationColumns("r", "replies") + ` FROM community_resources r JOIN auth_users u ON u.id=r.owner_id WHERE r.kind='reply'`,
		filters: []listFilter{statusFilter},
	}
)

// deleteContent hard-deletes one row by id; the foreign keys cascade to downloads, ratings and files.
func deleteContent(query string) adminActionFunc {
	return func(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
		if err := requireActionID(v); err != nil {
			return actionResult{}, err
		}
		tag, err := tx.Exec(ctx, query, v.ID)
		if err != nil {
			return actionResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return actionResult{}, actionFail(404, "not_found")
		}
		return actionResult{Affected: tag.RowsAffected()}, nil
	}
}

var (
	actionDeleteSkin          = deleteContent(`DELETE FROM community_skins WHERE id=$1`)
	actionDeleteCandidateSkin = deleteContent(`DELETE FROM community_candidate_skins WHERE id=$1`)
	actionDeletePlugin        = deleteContent(`DELETE FROM community_plugins WHERE id=$1`)
	actionDeleteDictionary    = deleteContent(`DELETE FROM community_resources WHERE id=$1 AND kind='dictionary'`)
	actionDeleteReply         = deleteContent(`DELETE FROM community_resources WHERE id=$1 AND kind='reply'`)
)

// moderationTable is where one admin section's rows live; kind narrows the shared resources table and label names the content kind in notifications.
type moderationTable struct {
	table, kind, label string
}

// moderationSections maps the admin section names (also the community_reports kinds) to their tables.
var moderationSections = map[string]moderationTable{
	"skins":           {"community_skins", "", "皮肤"},
	"candidate-skins": {"community_candidate_skins", "", "候选皮肤"},
	"plugins":         {"community_plugins", "", "插件"},
	"dictionaries":    {"community_resources", "dictionary", "词库"},
	"replies":         {"community_resources", "reply", "回复模板"},
}

// match is the WHERE clause selecting the ids in $1 of this section; every identifier is a fixed string from moderationSections.
func (m moderationTable) match() string { return m.where(`id=ANY($1)`) }

// where is the WHERE clause adding this section's kind to condition, a fixed SQL fragment.
func (m moderationTable) where(condition string) string {
	where := ` WHERE ` + condition
	if m.kind != "" {
		where += ` AND kind='` + m.kind + `'`
	}
	return where
}

// moderationRequest resolves the section and the target ids (ids, or id when ids is empty) of a moderation action.
func moderationRequest(v actionRequest) (moderationTable, []string, error) {
	section, ok := moderationSections[v.Section]
	if !ok {
		return section, nil, actionFail(400, "invalid_section")
	}
	ids := v.IDs
	if len(ids) == 0 {
		if err := requireActionID(v); err != nil {
			return section, nil, err
		}
		ids = []string{v.ID}
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	return section, slices.Compact(ids), nil
}

// moderationUpdate runs one moderation UPDATE that ends in RETURNING id,name and collects the rows it changed.
func moderationUpdate(ctx context.Context, tx pgx.Tx, query string, args ...any) (changed []moderatedItem, err error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item moderatedItem
		if err = rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		changed = append(changed, item)
	}
	return changed, rows.Err()
}

// moderatedItem is one row a moderation action changed.
type moderatedItem struct{ ID, Name string }

// moderationResult audits a moderation change with its section, count, reason and the changed ids (plus the name when there is one item, for the console's activity text), targeting the section.
func moderationResult(v actionRequest, changed []moderatedItem, extra map[string]any) actionResult {
	ids := make([]string, len(changed))
	for i, item := range changed {
		ids[i] = item.ID
	}
	slices.Sort(ids)
	affected := int64(len(changed))
	detail := map[string]any{"section": v.Section, "count": affected, "reason": strings.TrimSpace(v.Reason), "ids": ids}
	if len(changed) == 1 {
		detail["name"] = changed[0].Name
	}
	for k, value := range extra {
		detail[k] = value
	}
	return actionResult{Affected: affected, Target: v.Section, Detail: detail}
}

// bannedOwnerGuard fails with 409 owner_banned when a removed row among ids belongs to a banned account: the ban removed it, and only unbanning the account brings it back, so a moderator cannot republish a banned author's work.
func bannedOwnerGuard(ctx context.Context, tx pgx.Tx, section moderationTable, ids []string) error {
	var banned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+section.table+section.match()+` AND moderation='removed' AND EXISTS(SELECT 1 FROM auth_users u WHERE u.id=owner_id AND u.banned_at IS NOT NULL))`, ids).Scan(&banned); err != nil {
		return err
	}
	if banned {
		return actionFail(409, "owner_banned")
	}
	return nil
}

// actionApproveContent approves the items ids (or id) of section. A pending row keeps its automatic flag in moderation_reason, so undoing the approval brings the warning back; a removed row's removal reason is cleared.
func actionApproveContent(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	section, ids, err := moderationRequest(v)
	if err != nil {
		return actionResult{}, err
	}
	if err = bannedOwnerGuard(ctx, tx, section, ids); err != nil {
		return actionResult{}, err
	}
	changed, err := moderationUpdate(ctx, tx, `UPDATE `+section.table+` SET moderation='approved',previous_moderation=NULL,moderation_reason=CASE WHEN moderation='pending' THEN moderation_reason END,moderated_by=$2,moderated_at=now()`+section.match()+` RETURNING id,name`, ids, adminActor(ctx))
	if err != nil {
		return actionResult{}, err
	}
	if len(changed) == 0 {
		return actionResult{}, actionFail(404, "not_found")
	}
	return moderationResult(v, changed, nil), nil
}

// actionRemoveContent hides the items ids (or id) of section from the public endpoints, recording reason and the state it replaced.
func actionRemoveContent(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	section, ids, err := moderationRequest(v)
	if err != nil {
		return actionResult{}, err
	}
	reason := strings.TrimSpace(v.Reason)
	if reason == "" {
		return actionResult{}, actionFail(400, "invalid_reason")
	}
	// Removing a removed row again only updates the reason, so previous_moderation keeps the state the first removal replaced.
	changed, err := moderationUpdate(ctx, tx, `UPDATE `+section.table+` SET previous_moderation=CASE WHEN moderation='removed' THEN previous_moderation ELSE moderation END,moderation='removed',moderation_reason=$3,moderated_by=$2,moderated_at=now()`+section.match()+` RETURNING id,name`, ids, adminActor(ctx), reason)
	if err != nil {
		return actionResult{}, err
	}
	if len(changed) == 0 {
		return actionResult{}, actionFail(404, "not_found")
	}
	return moderationResult(v, changed, nil), nil
}

// actionRestoreContent undoes remove_content, putting each item back to its previous_moderation. With value {"to":"pending"|"approved"} it instead sets that state on items that are not removed, which is how the console undoes an approval; it never brings back an item someone removed in the meantime (409 conflict).
func actionRestoreContent(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	section, ids, err := moderationRequest(v)
	if err != nil {
		return actionResult{}, err
	}
	to := ""
	if len(v.Value) > 0 && string(v.Value) != "null" {
		var value struct {
			To string `json:"to"`
		}
		d := json.NewDecoder(bytes.NewReader(v.Value))
		d.DisallowUnknownFields()
		if d.Decode(&value) != nil || (value.To != "pending" && value.To != "approved") {
			return actionResult{}, actionFail(400, "invalid_value")
		}
		to = value.To
	}
	var changed []moderatedItem
	if to == "" {
		if err = bannedOwnerGuard(ctx, tx, section, ids); err != nil {
			return actionResult{}, err
		}
		changed, err = moderationUpdate(ctx, tx, `UPDATE `+section.table+` SET moderation=COALESCE(previous_moderation,'approved'),previous_moderation=NULL,moderation_reason=NULL,moderated_by=$2,moderated_at=now()`+section.match()+` AND moderation='removed' RETURNING id,name`, ids, adminActor(ctx))
	} else {
		// moderation_reason of a row that is not removed is the automatic flag, which stays.
		changed, err = moderationUpdate(ctx, tx, `UPDATE `+section.table+` SET moderation=$3,previous_moderation=NULL,moderated_by=$2,moderated_at=now()`+section.match()+` AND moderation<>'removed' RETURNING id,name`, ids, adminActor(ctx), to)
	}
	if err != nil {
		return actionResult{}, err
	}
	if len(changed) == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+section.table+section.match()+`)`, ids).Scan(&exists); err != nil {
			return actionResult{}, err
		}
		switch {
		case !exists:
			return actionResult{}, actionFail(404, "not_found")
		case to == "":
			return actionResult{}, actionFail(409, "not_removed")
		default:
			return actionResult{}, actionFail(409, "conflict")
		}
	}
	var extra map[string]any
	if to != "" {
		extra = map[string]any{"to": to}
	}
	return moderationResult(v, changed, extra), nil
}

// moderationCounts is the number of items per section and moderation state. Private candidate skins are left out: they are visible only to their owner, so they never wait for review.
type moderationCounts map[string]map[string]int

func (a *Service) moderationCounts(ctx context.Context) (moderationCounts, error) {
	counts := moderationCounts{}
	for section := range moderationSections {
		counts[section] = map[string]int{"pending": 0, "approved": 0, "removed": 0}
	}
	rows, err := a.store.pool.Query(ctx, `SELECT 'skins',moderation,count(*) FROM community_skins GROUP BY moderation
UNION ALL SELECT 'candidate-skins',moderation,count(*) FROM community_candidate_skins WHERE visibility='public' GROUP BY moderation
UNION ALL SELECT 'plugins',moderation,count(*) FROM community_plugins GROUP BY moderation
UNION ALL SELECT CASE kind WHEN 'dictionary' THEN 'dictionaries' ELSE 'replies' END,moderation,count(*) FROM community_resources GROUP BY kind,moderation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var section, state string
		var n int
		if err = rows.Scan(&section, &state, &n); err != nil {
			return nil, err
		}
		if counts[section] != nil {
			counts[section][state] += n
		}
	}
	return counts, rows.Err()
}

// adminCommunityCounts serves GET /api/community/counts.
func (a *Service) adminCommunityCounts(w http.ResponseWriter, r *http.Request, _ string) {
	counts, err := a.moderationCounts(r.Context())
	if err != nil {
		a.error(w, err)
		return
	}
	write(w, 200, counts)
}

// adminCandidateSkinPreview serves GET /api/candidate-skins/{id}/preview: the preview image bytes, so the console can show them under img-src 'self'. Private rows are included, like the rest of the admin candidate-skin API. The bytes were re-encoded on upload.
func (a *Service) adminCandidateSkinPreview(w http.ResponseWriter, r *http.Request, id string) {
	if !resourceText(id, 1, 128, false) || strings.Contains(id, "/") {
		writeError(w, 400, "invalid_id")
		return
	}
	var path string
	var data []byte
	err := a.store.pool.QueryRow(r.Context(), `SELECT f.path,f.bytes FROM community_candidate_skins s JOIN community_candidate_skin_files f ON f.skin_id=s.id AND f.path=s.preview_path WHERE s.id=$1`, id).Scan(&path, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if err != nil {
		a.error(w, err)
		return
	}
	extension := candidateExtension(path)
	if extension == "" {
		writeError(w, 404, "not_found")
		return
	}
	w.Header().Set("Content-Type", "image/"+extension)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// PendingCommunity counts community items awaiting review, for the console shell's badge.
func (a *Service) PendingCommunity(ctx context.Context) (int, error) {
	counts, err := a.moderationCounts(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, states := range counts {
		total += states["pending"]
	}
	return total, nil
}

// screenCommunityText runs an upload's text through the sensitive-word matcher: blocked reports a block-level hit, and flag is the warning stored with a review-level hit (nil when nothing needs review).
func screenCommunityText(ctx context.Context, matcher SensitiveMatcher, texts ...string) (blocked bool, flag *string, err error) {
	hits, err := matcher.Match(ctx, strings.Join(texts, "\n"))
	if err != nil {
		return false, nil, err
	}
	var review []string
	for _, hit := range hits {
		switch hit.Level {
		case SensitiveBlock:
			return true, nil, nil
		case SensitiveReview:
			if !slices.Contains(review, hit.Pattern) {
				review = append(review, hit.Pattern)
			}
		}
	}
	if len(review) == 0 {
		return false, nil, nil
	}
	text := "命中敏感词：「" + strings.Join(review, "」「") + "」"
	if utf8.RuneCountInString(text) > 500 {
		text = string([]rune(text)[:499]) + "…"
	}
	return false, &text, nil
}

// screenUpload screens an upload's text and answers a block-level hit with 422 blocked_content; ok false means the response is written. The returned flag goes into moderation_reason of the pending row.
func (a *Service) screenUpload(w http.ResponseWriter, r *http.Request, texts ...string) (flag *string, ok bool) {
	blocked, flag, err := screenCommunityText(r.Context(), a.Sensitive(), texts...)
	if err != nil {
		a.error(w, err)
		return nil, false
	}
	if blocked {
		writeError(w, 422, "blocked_content")
		return nil, false
	}
	return flag, true
}

// reviewAgain is the SET fragment an author's edit applies: the item goes back to pending with the new automatic flag in the placeholder flag, unless a moderator removed it, which the edit does not undo; a removed item's restore state becomes pending, so restoring it later never publishes the unreviewed edit as approved.
func reviewAgain(flag string) string {
	return `moderation=CASE WHEN moderation='removed' THEN 'removed' ELSE 'pending' END,previous_moderation=CASE WHEN moderation='removed' THEN 'pending' END,moderation_reason=CASE WHEN moderation='removed' THEN moderation_reason ELSE ` + flag + ` END`
}

// resourceScreenText is the user-visible text of a word pack or reply template: the words and the prompt.
func resourceScreenText(content ResourceContent) string {
	parts := make([]string, 0, len(content.Entries)+1)
	for _, entry := range content.Entries {
		parts = append(parts, entry.Word)
	}
	if content.Prompt != "" {
		parts = append(parts, content.Prompt)
	}
	return strings.Join(parts, "\n")
}
