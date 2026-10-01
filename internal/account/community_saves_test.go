package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// savedKind 描述一种可收藏的社区条目，供下面三组收藏测试共用：base 是接口前缀，listKey 是列表响应里条目数组的键，saves/column 是收藏表及其指向条目的列，insert 直接写入一条满足所有约束的条目。
type savedKind struct {
	base, listKey, table, saves, column, notFound, ownRatingCode string
	insert                                                       func(t *testing.T, db *Store, id, owner, name string)
}

var (
	savedSkins = savedKind{base: "/v1/community/skins", listKey: "skins", table: "community_skins", saves: "community_skin_saves", column: "skin_id", notFound: "skin_not_found", ownRatingCode: "download_before_rating_or_own_skin",
		insert: func(t *testing.T, db *Store, id, owner, name string) {
			t.Helper()
			if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_skins(id,owner_id,name,design) VALUES($1,$2,$3,$4)`, id, owner, name, communityFixture); err != nil {
				t.Fatal(err)
			}
		}}
	savedCandidateSkins = savedKind{base: "/v1/community/candidate-skins", listKey: "skins", table: "community_candidate_skins", saves: "community_candidate_skin_saves", column: "skin_id", notFound: "skin_not_found", ownRatingCode: "download_before_rating_or_own_skin", insert: insertCandidateSkin}
	savedPlugins        = savedKind{base: "/v1/community/plugins", listKey: "plugins", table: "community_plugins", saves: "community_plugin_saves", column: "pack_id", notFound: "plugin_not_found", ownRatingCode: "download_before_rating_or_own_plugin",
		insert: func(t *testing.T, db *Store, id, owner, name string) {
			t.Helper()
			if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_plugins(id,owner_id,kind,plugin_id,name,version,license,manifest,archive,request_sha256) VALUES($1,$2,'sound','saved-pack',$3,'1.0','MIT','id = "saved-pack"','zip',$4)`, id, owner, name, hash(id)); err != nil {
				t.Fatal(err)
			}
		}}
)

// savedID 是第 n 条测试条目的 UUID，满足候选窗皮肤和插件表的 id 约束。
func savedID(n int) string { return fmt.Sprintf("5a7ed000-0000-4000-8000-%012d", n) }

func TestCommunitySkinSaves(t *testing.T)          { testCommunitySaves(t, savedSkins) }
func TestCommunityCandidateSkinSaves(t *testing.T) { testCommunitySaves(t, savedCandidateSkins) }
func TestCommunityPluginSaves(t *testing.T)        { testCommunitySaves(t, savedPlugins) }

// testCommunitySaves 覆盖一种条目的收藏：幂等的 PUT …/save、scope=saved 的鉴权、顺序和分页、fields=saved 的形状、不带 fields 时与收藏无关的逐字节相同响应、已下架作品的 404 与级联删除，以及放宽后的评分规则。
func testCommunitySaves(t *testing.T, k savedKind) {
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	owner := complete(t, db, Identity{"email", "saved-owner@example.test"})
	reader := complete(t, db, Identity{"email", "saved-reader@example.test"})
	// 22 条作品，发布时间依次变新，所以默认列表是 21、20 … 0。
	for n := range 22 {
		k.insert(t, db, savedID(n), owner.User.ID, fmt.Sprintf("作品%02d", n))
		if _, err := db.pool.Exec(t.Context(), `UPDATE `+k.table+` SET created_at=now()-make_interval(mins=>100-$2::int) WHERE id=$1`, savedID(n), n); err != nil {
			t.Fatal(err)
		}
	}
	list := func(query, token string, status int) (ids []string, more bool, raw []json.RawMessage) {
		t.Helper()
		w := apiRequest(t, mux, "GET", k.base+query, "", token, status)
		if status != 200 {
			return nil, false, nil
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || json.Unmarshal(body[k.listKey], &raw) != nil || json.Unmarshal(body["has_more"], &more) != nil {
			t.Fatal(w.Body.String(), err)
		}
		for _, item := range raw {
			var v struct{ ID string }
			if err := json.Unmarshal(item, &v); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, v.ID)
		}
		return ids, more, raw
	}
	save := func(n int, saved bool, token string, status int, want string) {
		t.Helper()
		w := apiRequest(t, mux, "PUT", k.base+"/"+savedID(n)+"/save", fmt.Sprintf(`{"saved":%v}`, saved), token, status)
		if want != "" && strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("save %d %v: %s, want %s", n, saved, w.Body.String(), want)
		}
	}
	anonymousDefault := apiRequest(t, mux, "GET", k.base, "", "", 200).Body.String()
	readerDefault := apiRequest(t, mux, "GET", k.base+"?scope=", "", reader.AccessToken, 200).Body.String()
	readerDetail := apiRequest(t, mux, "GET", k.base+"/"+savedID(3), "", reader.AccessToken, 200).Body.String()

	// 收藏需要会话，请求体要合法；重复收藏、重复取消都得到同一个结果。
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(3)+"/save", `{"saved":true}`, "", 401)
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(3)+"/save", `{"saved":"yes"}`, reader.AccessToken, 400)
	save(3, true, reader.AccessToken, 200, `{"saved":true,"saves":1}`)
	save(3, true, reader.AccessToken, 200, `{"saved":true,"saves":1}`)
	save(3, true, owner.AccessToken, 200, `{"saved":true,"saves":2}`)
	save(3, false, owner.AccessToken, 200, `{"saved":false,"saves":1}`)
	save(3, false, owner.AccessToken, 200, `{"saved":false,"saves":1}`)
	if w := apiRequest(t, mux, "PUT", k.base+"/"+savedID(99)+"/save", `{"saved":true}`, reader.AccessToken, 404); !strings.Contains(w.Body.String(), k.notFound) {
		t.Fatal(w.Body.String())
	}
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(99)+"/save", `{"saved":false}`, reader.AccessToken, 404)

	// 不带 fields 时，收藏前后、匿名与登录的响应都和以前逐字节相同。
	if got := apiRequest(t, mux, "GET", k.base, "", "", 200).Body.String(); got != anonymousDefault {
		t.Fatal("anonymous list changed after a save")
	}
	if got := apiRequest(t, mux, "GET", k.base+"?scope=", "", reader.AccessToken, 200).Body.String(); got != readerDefault {
		t.Fatal("reader list changed after a save")
	}
	if got := apiRequest(t, mux, "GET", k.base+"/"+savedID(3), "", reader.AccessToken, 200).Body.String(); got != readerDetail {
		t.Fatal("reader detail changed after a save")
	}
	if strings.Contains(readerDefault, `"saved"`) || strings.Contains(readerDefault, `"saves"`) || strings.Contains(readerDetail, `"saved"`) {
		t.Fatal("saved fields sent without fields=saved", readerDetail)
	}

	// scope=saved：只有自己收藏的，按收藏时间倒序，每页 20 条。
	list("?scope=saved", "", 401)
	if w := apiRequest(t, mux, "GET", k.base+"?scope=saved", "", "", 401); !strings.Contains(w.Body.String(), "user_session_required") {
		t.Fatal(w.Body.String())
	}
	list("?scope=other", reader.AccessToken, 400)
	if ids, more, _ := list("?scope=saved", owner.AccessToken, 200); len(ids) != 0 || more {
		t.Fatal("owner saved list", ids, more)
	}
	order := []int{5, 3, 0, 21, 7}
	for n := range 22 {
		if !slices.Contains(order, n) {
			order = append(order, n)
		}
	}
	for i, n := range order {
		save(n, true, reader.AccessToken, 200, "")
		// 收藏时间按 order 依次变新，与发布时间的先后无关。
		if _, err := db.pool.Exec(t.Context(), `UPDATE `+k.saves+` SET created_at=now()-make_interval(secs=>100-$3::int) WHERE `+k.column+`=$1 AND user_id=$2`, savedID(n), reader.User.ID, i); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for i := len(order) - 1; i >= 0; i-- {
		want = append(want, savedID(order[i]))
	}
	first, more, _ := list("?scope=saved", reader.AccessToken, 200)
	if !more || !slices.Equal(first, want[:20]) {
		t.Fatal("first saved page", first, more)
	}
	second, more, _ := list("?scope=saved&offset=20", reader.AccessToken, 200)
	if more || !slices.Equal(second, want[20:]) {
		t.Fatal("second saved page", second, more)
	}
	if ids, _, _ := list("?scope=saved&q=%E4%BD%9C%E5%93%8121", reader.AccessToken, 200); !slices.Equal(ids, []string{savedID(21)}) {
		t.Fatal("search inside saved", ids)
	}
	// 取消收藏后从列表消失。
	save(order[len(order)-1], false, reader.AccessToken, 200, "")
	if ids, _, _ := list("?scope=saved", reader.AccessToken, 200); ids[0] != want[1] {
		t.Fatal("unsaved item still listed", ids)
	}
	save(order[len(order)-1], true, reader.AccessToken, 200, "")

	// fields=saved：每个条目带 saved 和 saves，匿名时 saved 为 false；可与已有的 fields 值组合，未知值仍是 400。
	type savedItem struct {
		ID         string `json:"id"`
		Saved      *bool  `json:"saved"`
		Saves      *int   `json:"saves"`
		Moderation string `json:"moderation"`
	}
	decode := func(raw []json.RawMessage) []savedItem {
		items := make([]savedItem, len(raw))
		for i, r := range raw {
			if err := json.Unmarshal(r, &items[i]); err != nil {
				t.Fatal(err)
			}
		}
		return items
	}
	_, _, raw := list("?fields=saved", "", 200)
	// 此时每条作品都只被 reader 收藏过一次。
	for _, item := range decode(raw) {
		if item.Saved == nil || *item.Saved || item.Saves == nil || *item.Saves != 1 {
			t.Fatalf("anonymous fields=saved %+v", item)
		}
	}
	save(9, true, owner.AccessToken, 200, `{"saved":true,"saves":2}`)
	_, _, raw = list("?fields=moderation,saved", owner.AccessToken, 200)
	for _, item := range decode(raw) {
		if item.Saved == nil || *item.Saved != (item.ID == savedID(9)) || item.Saves == nil || item.Moderation == "" {
			t.Fatalf("owner fields=moderation,saved %+v", item)
		}
		if item.ID == savedID(9) && *item.Saves != 2 {
			t.Fatalf("saves count %+v", item)
		}
	}
	_, _, raw = list("?scope=saved&fields=saved", reader.AccessToken, 200)
	for _, item := range decode(raw) {
		if item.Saved == nil || !*item.Saved {
			t.Fatalf("reader saved list %+v", item)
		}
	}
	var detail savedItem
	if err := json.Unmarshal(apiRequest(t, mux, "GET", k.base+"/"+savedID(9)+"?fields=saved", "", reader.AccessToken, 200).Body.Bytes(), &detail); err != nil || detail.Saved == nil || !*detail.Saved || detail.Saves == nil || *detail.Saves != 2 || detail.Moderation != "" {
		t.Fatalf("detail fields=saved %+v %v", detail, err)
	}
	if err := json.Unmarshal(apiRequest(t, mux, "GET", k.base+"/"+savedID(9)+"?fields=saved", "", "", 200).Body.Bytes(), &detail); err != nil || detail.Saved == nil || *detail.Saved || *detail.Saves != 2 {
		t.Fatalf("anonymous detail fields=saved %+v %v", detail, err)
	}
	list("?fields=saved,bogus", "", 400)
	apiRequest(t, mux, "GET", k.base+"/"+savedID(9)+"?fields=bogus", "", "", 400)

	// 评分不再要求先下载；自己的作品仍是原来的 403 错误码，不存在的 404。
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(4)+"/rating", `{"stars":4}`, reader.AccessToken, 200)
	if w := apiRequest(t, mux, "PUT", k.base+"/"+savedID(4)+"/rating", `{"stars":5}`, owner.AccessToken, 403); !strings.Contains(w.Body.String(), k.ownRatingCode) {
		t.Fatal(w.Body.String())
	}
	if w := apiRequest(t, mux, "PUT", k.base+"/"+savedID(99)+"/rating", `{"stars":5}`, reader.AccessToken, 404); !strings.Contains(w.Body.String(), k.notFound) {
		t.Fatal(w.Body.String())
	}
	var rated struct {
		MyRating    int `json:"my_rating"`
		RatingCount int `json:"rating_count"`
		Downloads   int `json:"downloads"`
	}
	if err := json.Unmarshal(apiRequest(t, mux, "GET", k.base+"/"+savedID(4), "", reader.AccessToken, 200).Body.Bytes(), &rated); err != nil || rated.MyRating != 4 || rated.RatingCount != 1 || rated.Downloads != 0 {
		t.Fatalf("rating without download %+v %v", rated, err)
	}

	// 已下架：别人不能收藏、不能评分，已有的收藏不再列出；作者本人仍可收藏，评分仍是自己作品的 403。
	if _, err := db.pool.Exec(t.Context(), `UPDATE `+k.table+` SET moderation='removed' WHERE id=$1`, savedID(5)); err != nil {
		t.Fatal(err)
	}
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(5)+"/save", `{"saved":true}`, reader.AccessToken, 404)
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(5)+"/save", `{"saved":false}`, reader.AccessToken, 404)
	apiRequest(t, mux, "PUT", k.base+"/"+savedID(5)+"/rating", `{"stars":3}`, reader.AccessToken, 404)
	save(5, true, owner.AccessToken, 200, "")
	page1, _, _ := list("?scope=saved", reader.AccessToken, 200)
	page2, _, _ := list("?scope=saved&offset=20", reader.AccessToken, 200)
	if all := append(page1, page2...); len(all) != 21 || slices.Contains(all, savedID(5)) {
		t.Fatal("removed item listed", all)
	}
	if ids, _, _ := list("?scope=saved", owner.AccessToken, 200); !slices.Contains(ids, savedID(5)) {
		t.Fatal("owner's own removed save missing", ids)
	}

	// 删除作品时收藏随之级联删除。
	if _, err := db.pool.Exec(t.Context(), `DELETE FROM `+k.table+` WHERE id=$1`, savedID(3)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM `+k.saves+` WHERE `+k.column+`=$1`, savedID(3)).Scan(&left); err != nil || left != 0 {
		t.Fatal("saves not cascaded", left, err)
	}
}

// 候选窗皮肤的私有作品：别人收藏、评分都是 404；作者本人可以收藏，只在带 fields=sync 的 saved 列表里看到它。
func TestCommunityCandidateSkinSavesPrivate(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	owner := complete(t, db, Identity{"email", "private-owner@example.test"})
	reader := complete(t, db, Identity{"email", "private-reader@example.test"})
	private := savedID(1)
	insertCandidateSkin(t, db, private, owner.User.ID, "私有")
	if _, err := db.pool.Exec(t.Context(), `UPDATE community_candidate_skins SET visibility='private' WHERE id=$1`, private); err != nil {
		t.Fatal(err)
	}
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+private+"/save", `{"saved":true}`, reader.AccessToken, 404)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+private+"/rating", `{"stars":5}`, reader.AccessToken, 404)
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+private+"/save", `{"saved":true}`, owner.AccessToken, 200)
	if w := apiRequest(t, mux, "GET", "/v1/community/candidate-skins?scope=saved", "", owner.AccessToken, 200); strings.Contains(w.Body.String(), private) {
		t.Fatal("private row sent to a client that did not opt in", w.Body.String())
	}
	if w := apiRequest(t, mux, "GET", "/v1/community/candidate-skins?scope=saved&fields=sync,saved", "", owner.AccessToken, 200); !strings.Contains(w.Body.String(), private) || !strings.Contains(w.Body.String(), `"saved":true`) {
		t.Fatal("owner's private save missing", w.Body.String())
	}
	// 收藏之后作品被作者设为私有：收藏者的列表不再出现它，也不能评分。
	public := savedID(2)
	insertCandidateSkin(t, db, public, owner.User.ID, "公开")
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+public+"/save", `{"saved":true}`, reader.AccessToken, 200)
	if _, err := db.pool.Exec(t.Context(), `UPDATE community_candidate_skins SET visibility='private' WHERE id=$1`, public); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"?scope=saved", "?scope=saved&fields=sync,saved"} {
		if w := apiRequest(t, mux, "GET", "/v1/community/candidate-skins"+query, "", reader.AccessToken, 200); strings.Contains(w.Body.String(), public) {
			t.Fatal("someone else's private row listed", query)
		}
	}
	apiRequest(t, mux, "PUT", "/v1/community/candidate-skins/"+public+"/rating", `{"stars":5}`, reader.AccessToken, 404)
}

// 词库和回复模板的评分不再要求先收藏；自己的作品仍是 403 save_before_rating_or_own_resource，不存在和已下架的是 404。
func TestResourceRatingWithoutSave(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	owner := complete(t, db, Identity{"email", "resource-owner@example.test"})
	reader := complete(t, db, Identity{"email", "resource-reader@example.test"})
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_resources(id,owner_id,kind,name,content) VALUES('reply-rated',$1,'reply','回复','{"prompt":"好的"}'),('reply-removed',$1,'reply','下架','{"prompt":"好的"}')`, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE community_resources SET moderation='removed' WHERE id='reply-removed'`); err != nil {
		t.Fatal(err)
	}
	apiRequest(t, mux, "PUT", "/v1/community/resources/reply-rated/rating", `{"stars":4}`, reader.AccessToken, 200)
	if w := apiRequest(t, mux, "PUT", "/v1/community/resources/reply-rated/rating", `{"stars":4}`, owner.AccessToken, 403); !strings.Contains(w.Body.String(), "save_before_rating_or_own_resource") {
		t.Fatal(w.Body.String())
	}
	apiRequest(t, mux, "PUT", "/v1/community/resources/reply-removed/rating", `{"stars":4}`, owner.AccessToken, 403)
	for _, id := range []string{"reply-removed", "missing"} {
		if w := apiRequest(t, mux, "PUT", "/v1/community/resources/"+id+"/rating", `{"stars":4}`, reader.AccessToken, 404); !strings.Contains(w.Body.String(), "resource_not_found") {
			t.Fatal(id, w.Body.String())
		}
	}
	var item CommunityResource
	if err := json.NewDecoder(bytes.NewReader(apiRequest(t, mux, "GET", "/v1/community/resources/reply-rated", "", reader.AccessToken, 200).Body.Bytes())).Decode(&item); err != nil || item.MyRating != 4 || item.Saved {
		t.Fatalf("%+v %v", item, err)
	}
}
