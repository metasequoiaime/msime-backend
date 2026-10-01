package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Community moderation page (unit U2): the five content lists, deletion, approve/remove/restore and the pending counts.

// The lists serve GET /api/{skins,candidate-skins,plugins,dictionaries,replies}.
var (
	skinsList = adminList{
		query: `SELECT s.id,s.name,s.description,s.owner_id,s.created_at,(SELECT count(*) FROM community_skin_downloads WHERE skin_id=s.id) AS downloads FROM community_skins s`,
	}
	// Candidate skins hold both the public gallery and each account's private library, so moderation can list either one.
	candidateSkinsList = adminList{
		query:   `SELECT s.id,s.package_id,s.name,COALESCE(NULLIF(btrim(u.display_name),''),'水杉小鹿·'||upper(left(u.id,6))) AS author,(SELECT COALESCE(sum(size),0) FROM community_candidate_skin_files WHERE skin_id=s.id) AS size,(SELECT count(*) FROM community_candidate_skin_files WHERE skin_id=s.id) AS file_count,s.visibility,s.created_at,s.updated_at FROM community_candidate_skins s JOIN auth_users u ON u.id=s.owner_id`,
		filters: []listFilter{{param: "visibility", field: "visibility", max: 7, values: map[string]string{"public": "public", "private": "private"}}},
	}
	pluginsList = adminList{
		query: `SELECT p.id,p.kind,p.plugin_id,p.name,p.version,COALESCE(NULLIF(btrim(u.display_name),''),'水杉小鹿·'||upper(left(u.id,6))) AS author,p.owner_id,p.size,p.sha256,(SELECT count(*) FROM community_plugin_downloads WHERE pack_id=p.id) AS downloads,p.created_at FROM community_plugins p JOIN auth_users u ON u.id=p.owner_id`,
	}
	dictionariesList = adminList{
		query: `SELECT id,name,description,owner_id,revision,created_at,updated_at,jsonb_array_length(content->'entries') AS entries,(SELECT count(*) FROM community_resource_saves WHERE resource_id=community_resources.id) AS saves FROM community_resources WHERE kind='dictionary'`,
	}
	repliesList = adminList{
		query: `SELECT id,name,description,owner_id,revision,created_at,updated_at,content->>'prompt' AS prompt FROM community_resources WHERE kind='reply'`,
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

// actionApproveContent approves the items ids (or id) of section.
func actionApproveContent(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionRemoveContent hides the items ids (or id) of section from the public endpoints, recording reason and the state it replaced.
func actionRemoveContent(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionRestoreContent undoes remove_content, putting each item back to its previous_moderation.
func actionRestoreContent(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// adminCommunityCounts serves GET /api/community/counts.
func (a *Service) adminCommunityCounts(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// adminCandidateSkinPreview serves GET /api/candidate-skins/{id}/preview: the preview image bytes, so the console can show them under img-src 'self'.
func (a *Service) adminCandidateSkinPreview(w http.ResponseWriter, r *http.Request, id string) {
	notImplemented(w)
}

// PendingCommunity counts community items awaiting review, for the console shell's badge. It reports 0 until moderation is implemented.
func (a *Service) PendingCommunity(ctx context.Context) (int, error) {
	return 0, nil
}
