package account

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// adminContent reads public community content only, using fixed queries per kind.
func (a *Service) adminContent(w http.ResponseWriter, r *http.Request, section, id string) {
	if !resourceText(id, 1, 128, false) || strings.Contains(id, "/") {
		writeError(w, 400, "invalid_id")
		return
	}
	var query string
	args := []any{id}
	if section == "skins" {
		query = `SELECT json_build_object('id',s.id,'name',s.name,'description',s.description,'owner_id',s.owner_id,'author',u.display_name,'created_at',s.created_at,'content',s.design,
 'downloads',(SELECT count(*) FROM community_skin_downloads WHERE skin_id=s.id),
 'rating_count',(SELECT count(*) FROM community_skin_ratings WHERE skin_id=s.id),
 'rating_average',(SELECT COALESCE(avg(stars),0) FROM community_skin_ratings WHERE skin_id=s.id))
 FROM community_skins s JOIN auth_users u ON u.id=s.owner_id WHERE s.id=$1`
	} else if section == "candidate-skins" {
		// Metadata, the manifest text and per-file digests only; image bytes never leave the database through the admin API.
		query = `SELECT json_build_object('id',s.id,'package_id',s.package_id,'name',s.name,'description',s.description,'owner_id',s.owner_id,'author',u.display_name,'created_at',s.created_at,'version',s.version,
 'license',json_build_object('code',s.license_code,'assets',s.license_assets,'source',s.license_source),'preview',s.preview_path,'content',convert_from(s.manifest,'UTF8'),
 'files',COALESCE((SELECT json_agg(json_build_object('path',f.path,'size',f.size,'sha256',f.sha256) ORDER BY f.path) FROM community_candidate_skin_files f WHERE f.skin_id=s.id),'[]'::json),
 'downloads',(SELECT count(*) FROM community_candidate_skin_downloads WHERE skin_id=s.id),
 'rating_count',(SELECT count(*) FROM community_candidate_skin_ratings WHERE skin_id=s.id),
 'rating_average',(SELECT COALESCE(avg(stars),0) FROM community_candidate_skin_ratings WHERE skin_id=s.id))
 FROM community_candidate_skins s JOIN auth_users u ON u.id=s.owner_id WHERE s.id=$1`
	} else {
		kind := "dictionary"
		if section == "replies" {
			kind = "reply"
		}
		args = append(args, kind)
		query = `SELECT json_build_object('id',s.id,'name',s.name,'description',s.description,'owner_id',s.owner_id,'author',u.display_name,'created_at',s.created_at,'updated_at',s.updated_at,'revision',s.revision,'content',s.content,
 'saves',(SELECT count(*) FROM community_resource_saves WHERE resource_id=s.id),
 'rating_count',(SELECT count(*) FROM community_resource_ratings WHERE resource_id=s.id),
 'rating_average',(SELECT COALESCE(avg(stars),0) FROM community_resource_ratings WHERE resource_id=s.id))
 FROM community_resources s JOIN auth_users u ON u.id=s.owner_id WHERE s.id=$1 AND s.kind=$2`
	}
	var result json.RawMessage
	err := a.store.pool.QueryRow(r.Context(), query, args...).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if err != nil {
		a.error(w, err)
		return
	}
	write(w, 200, result)
}
