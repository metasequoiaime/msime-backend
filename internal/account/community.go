package account

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/jpeg"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// This versioned data-only format matches iOS CustomKeyboardSkin v1; it contains no executable resources or remote URLs.
type CommunityDesign struct {
	KeyShape           string   `json:"keyShape,omitempty"`
	KeyMaterial        string   `json:"keyMaterial,omitempty"`
	Background         uint32   `json:"background"`
	KeyBackground      uint32   `json:"keyBackground"`
	KeyForeground      uint32   `json:"keyForeground"`
	Accent             uint32   `json:"accent"`
	ActionBackground   uint32   `json:"actionBackground"`
	CornerRadius       float64  `json:"cornerRadius"`
	BorderWidth        float64  `json:"borderWidth"`
	Shadow             float64  `json:"shadow"`
	Pattern            int      `json:"pattern"`
	Monospaced         bool     `json:"monospaced"`
	KeyOpacity         *float64 `json:"keyOpacity,omitempty"`
	GradientEnd        *uint32  `json:"gradientEnd,omitempty"`
	GradientHorizontal *bool    `json:"gradientHorizontal,omitempty"`
	PatternOpacity     *float64 `json:"patternOpacity,omitempty"`
	CustomBorderColor  *uint32  `json:"customBorderColor,omitempty"`
	Photo              []byte   `json:"photo,omitempty"`
	PhotoShade         *float64 `json:"photoShade,omitempty"`
	PhotoPosition      *float64 `json:"photoPosition,omitempty"`
}

func validRange(value, min, max float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= min && value <= max
}
func parseCommunityDesign(raw json.RawMessage) (CommunityDesign, error) {
	var v CommunityDesign
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return v, ErrInvalid
	}
	for _, key := range []string{"background", "keyBackground", "keyForeground", "accent", "actionBackground", "cornerRadius", "borderWidth", "shadow", "pattern", "monospaced"} {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return v, ErrInvalid
		}
	}
	switch v.KeyShape {
	case "", "rounded", "capsule", "ticket", "pebble":
	default:
		return v, ErrInvalid
	}
	switch v.KeyMaterial {
	case "", "flat", "raised", "glass", "paper":
	default:
		return v, ErrInvalid
	}
	for _, c := range []uint32{v.Background, v.KeyBackground, v.KeyForeground, v.Accent, v.ActionBackground} {
		if c > 0xffffff {
			return v, ErrInvalid
		}
	}
	for _, c := range []*uint32{v.GradientEnd, v.CustomBorderColor} {
		if c != nil && *c > 0xffffff {
			return v, ErrInvalid
		}
	}
	if !validRange(v.CornerRadius, 0, 20) || !validRange(v.BorderWidth, 0, 2) || !validRange(v.Shadow, 0, 0.4) || v.Pattern < 0 || v.Pattern > 3 {
		return v, ErrInvalid
	}
	for _, r := range []struct {
		p        *float64
		min, max float64
	}{{v.KeyOpacity, .25, 1}, {v.PatternOpacity, 0, .5}, {v.PhotoShade, 0, .8}, {v.PhotoPosition, 0, 1}} {
		if r.p != nil && !validRange(*r.p, r.min, r.max) {
			return v, ErrInvalid
		}
	}
	if len(v.Photo) > 0 {
		if len(v.Photo) > 512000 {
			return v, ErrInvalid
		}
		config, err := jpeg.DecodeConfig(bytes.NewReader(v.Photo))
		if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 1024 || config.Height > 1024 {
			return v, ErrInvalid
		}
		image, err := jpeg.Decode(bytes.NewReader(v.Photo))
		if err != nil {
			return v, ErrInvalid
		}
		var clean bytes.Buffer
		if jpeg.Encode(&clean, image, &jpeg.Options{Quality: 80}) != nil || clean.Len() > 512000 {
			return v, ErrInvalid
		}
		v.Photo = clean.Bytes()
	}
	return v, nil
}

type CommunitySkin struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Author        string          `json:"author"`
	Design        json.RawMessage `json:"design"`
	Downloads     int             `json:"downloads"`
	RatingCount   int             `json:"rating_count"`
	RatingAverage float64         `json:"rating_average"`
	Owned         bool            `json:"owned"`
	MyRating      int             `json:"my_rating"`
	// Moderation 是作品的审核状态（approved、pending 或 removed），只出现在作者自己的作品上，且只在请求带了 `fields=moderation` 时出现（见 communityFields）。
	Moderation string `json:"moderation,omitempty"`
	// moderation 是为作者读出的状态，其他人为空。
	moderation string
}

// communityFields 读取社区列表和详情接口的 `fields=` 开关：逗号分隔、取自 allowed 的名称列表，出现其他名称时 ok 为 false。已发布的客户端解析社区条目时拒绝未知字段，所以额外字段只发给显式请求的客户端。
func communityFields(r *http.Request, allowed ...string) (fields map[string]bool, ok bool) {
	fields = map[string]bool{}
	raw := r.URL.Query().Get("fields")
	if raw == "" {
		return fields, true
	}
	for _, name := range strings.Split(raw, ",") {
		if !slices.Contains(allowed, name) {
			return nil, false
		}
		fields[name] = true
	}
	return fields, true
}

// ownerModeration 返回条目的 moderation 值：客户端请求了 `fields=moderation` 时为读给作者的状态，否则为空，字段因此省略。
func ownerModeration(fields map[string]bool, state string) string {
	if fields["moderation"] {
		return state
	}
	return ""
}

const communitySelect = `SELECT s.id,s.name,s.description,
 COALESCE(NULLIF(btrim(u.display_name),''),'水杉小鹿·'||upper(left(u.id,6))),s.design-'photo',
 (SELECT count(*) FROM community_skin_downloads WHERE skin_id=s.id),
 (SELECT count(*) FROM community_skin_ratings WHERE skin_id=s.id),
 COALESCE((SELECT avg(stars) FROM community_skin_ratings WHERE skin_id=s.id),0),
 s.owner_id=$1,COALESCE((SELECT stars FROM community_skin_ratings WHERE skin_id=s.id AND user_id=$1),0),
 CASE WHEN s.owner_id=$1 THEN s.moderation ELSE '' END
 FROM community_skins s JOIN auth_users u ON u.id=s.owner_id `

func scanSkin(row interface{ Scan(...any) error }) (CommunitySkin, error) {
	var s CommunitySkin
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Author, &s.Design, &s.Downloads, &s.RatingCount, &s.RatingAverage, &s.Owned, &s.MyRating, &s.moderation)
	return s, err
}

// Browsing is public, but personalized fields only use a validated account session.
func (a *Service) communityViewer(r *http.Request) string {
	p, e := a.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if e != nil {
		return ""
	}
	return p.UserID
}
func (a *Service) communityList(w http.ResponseWriter, r *http.Request) {
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 100000 {
			writeError(w, 400, "invalid_offset")
			return
		}
		offset = n
	}
	search := r.URL.Query().Get("q")
	if !utf8.ValidString(search) || len(search) > 128 {
		writeError(w, 400, "invalid_search")
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != "mine" {
		writeError(w, 400, "invalid_scope")
		return
	}
	fields, ok := communityFields(r, "moderation")
	if !ok {
		writeError(w, 400, "invalid_fields")
		return
	}
	viewer := a.communityViewer(r)
	if scope == "mine" && viewer == "" {
		writeError(w, 401, "user_session_required")
		return
	}
	rows, e := a.store.pool.Query(r.Context(), communitySelect+`WHERE strpos(lower(s.name),lower($2))>0 AND ($4='' OR s.owner_id=$1) AND (s.moderation<>'removed' OR s.owner_id=$1) ORDER BY s.created_at DESC,s.id LIMIT 21 OFFSET $3`, viewer, search, offset, scope)
	if e != nil {
		a.error(w, e)
		return
	}
	defer rows.Close()
	items := []CommunitySkin{}
	for rows.Next() {
		v, e := scanSkin(rows)
		if e != nil {
			a.error(w, e)
			return
		}
		v.Moderation = ownerModeration(fields, v.moderation)
		items = append(items, v)
	}
	if e = rows.Err(); e != nil {
		a.error(w, e)
		return
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	write(w, 200, map[string]any{"skins": items, "has_more": more})
}
func (a *Service) communityDetail(w http.ResponseWriter, r *http.Request) {
	fields, ok := communityFields(r, "moderation")
	if !ok {
		writeError(w, 400, "invalid_fields")
		return
	}
	v, e := scanSkin(a.store.pool.QueryRow(r.Context(), communitySelect+`WHERE s.id=$2 AND (s.moderation<>'removed' OR s.owner_id=$1)`, a.communityViewer(r), r.PathValue("id")))
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "skin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	// Full wallpaper only travels with the detail/download response, never the catalog.
	if e = a.store.pool.QueryRow(r.Context(), `SELECT design FROM community_skins WHERE id=$1`, v.ID).Scan(&v.Design); e != nil {
		a.error(w, e)
		return
	}
	v.Moderation = ownerModeration(fields, v.moderation)
	write(w, 200, v)
}
func (a *Service) communityPublish(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	var input struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Design      json.RawMessage `json:"design"`
	}
	if !readSized(w, r, &input, 710000) {
		return
	}
	input.ID = strings.ToLower(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if len(input.ID) != 36 || !validCommunityID(input.ID) || utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 32 || utf8.RuneCountInString(input.Description) > 280 {
		writeError(w, 400, "invalid_skin_metadata")
		return
	}
	design, e := parseCommunityDesign(input.Design)
	if e != nil {
		writeError(w, 400, "invalid_skin_design")
		return
	}
	raw, e := json.Marshal(design)
	if e != nil {
		a.error(w, e)
		return
	}
	// Stable client UUID makes publication retry safe, and never changes someone else's work. A published skin never changes, so a retry is answered before screening: a word added to the list since then cannot turn the retry of live content into 422 or count its hits again. The probe in the transaction stays authoritative.
	var existingOwner string
	var identical bool
	e = a.store.pool.QueryRow(r.Context(), `SELECT owner_id, name=$2 AND description=$3 AND design=$4::jsonb FROM community_skins WHERE id=$1`, input.ID, input.Name, input.Description, raw).Scan(&existingOwner, &identical)
	if e == nil {
		if existingOwner != p.UserID || !identical {
			writeError(w, 409, "skin_id_conflict")
			return
		}
		write(w, 200, map[string]string{"id": input.ID})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		a.error(w, e)
		return
	}
	flag, ok := a.screenUpload(w, r, input.Name, input.Description)
	if !ok {
		return
	}
	tx, e := a.store.userDataTransaction(r.Context(), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	e = tx.QueryRow(r.Context(), `SELECT owner_id, name=$2 AND description=$3 AND design=$4::jsonb FROM community_skins WHERE id=$1`, input.ID, input.Name, input.Description, raw).Scan(&existingOwner, &identical)
	if e == nil {
		if existingOwner != p.UserID || !identical {
			writeError(w, 409, "skin_id_conflict")
			return
		}
		write(w, 200, map[string]string{"id": input.ID})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		a.error(w, e)
		return
	}
	var count int
	if e = tx.QueryRow(r.Context(), `SELECT count(*) FROM community_skins WHERE owner_id=$1`, p.UserID).Scan(&count); e != nil {
		a.error(w, e)
		return
	}
	if count >= 50 {
		writeError(w, 409, "skin_publish_limit")
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO community_skins(id,owner_id,name,description,design,moderation,moderation_reason) VALUES($1,$2,$3,$4,$5,'pending',$6)`, input.ID, p.UserID, input.Name, input.Description, raw, flag)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 201, map[string]string{"id": input.ID})
}
func validCommunityID(v string) bool {
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func (a *Service) communityDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	result, e := a.store.pool.Exec(r.Context(), `DELETE FROM community_skins WHERE id=$1 AND owner_id=$2`, r.PathValue("id"), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, 404, "skin_not_found")
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (a *Service) communityDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	tx, e := a.store.pool.Begin(r.Context())
	if e != nil {
		a.error(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	var design json.RawMessage
	e = tx.QueryRow(r.Context(), `SELECT design FROM community_skins WHERE id=$1 AND (moderation<>'removed' OR owner_id=$2) FOR SHARE`, r.PathValue("id"), p.UserID).Scan(&design)
	if errors.Is(e, pgx.ErrNoRows) {
		writeError(w, 404, "skin_not_found")
		return
	}
	if e != nil {
		a.error(w, e)
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO community_skin_downloads(skin_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.PathValue("id"), p.UserID)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, map[string]any{"design": design})
}
func (a *Service) communityRate(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	var input struct {
		Stars int `json:"stars"`
	}
	if !read(w, r, &input) {
		return
	}
	if input.Stars < 1 || input.Stars > 5 {
		writeError(w, 400, "invalid_rating")
		return
	}
	result, e := a.store.pool.Exec(r.Context(), `INSERT INTO community_skin_ratings(skin_id,user_id,stars)
 SELECT s.id,$2,$3 FROM community_skins s WHERE s.id=$1 AND s.owner_id<>$2 AND s.moderation<>'removed' AND EXISTS(SELECT 1 FROM community_skin_downloads WHERE skin_id=s.id AND user_id=$2)
 ON CONFLICT(skin_id,user_id) DO UPDATE SET stars=excluded.stars`, r.PathValue("id"), p.UserID, input.Stars)
	if e != nil {
		a.error(w, e)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, 403, "download_before_rating_or_own_skin")
		return
	}
	write(w, 200, map[string]int{"stars": input.Stars})
}
