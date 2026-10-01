package account

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/metasequoiaime/MSIME-Backend/internal/engine"
)

func TestNativeRankingPersistsCountersAndWeights(t *testing.T) {
	cfg := engine.Config{Binary: os.Getenv("MSIME_ENGINE_TEST_BINARY"), Resources: os.Getenv("MSIME_ENGINE_TEST_RESOURCES")}
	if cfg.Binary == "" || cfg.Resources == "" {
		t.Skip("需要真实 Engine 与发布词库")
	}
	s := testStore(t)
	ctx := context.Background()
	one := complete(t, s, Identity{"email", "ranking-one@example.test"})
	two := complete(t, s, Identity{"email", "ranking-two@example.test"})
	query := map[string]any{"operation": "candidates", "text": "nihao", "scheme": "pinyin", "profile": "xiaohe", "limit": 20}
	readCandidates := func(user string) []struct {
		Code      string `json:"code"`
		Canonical string `json:"canonical_pinyin"`
		Word      string `json:"word"`
		Weight    int64  `json:"weight"`
	} {
		t.Helper()
		raw, err := cfg.QuerySnapshot(ctx, map[string]any{"operation": "personal_query", "query": query}, func(ctx context.Context, w io.Writer) error {
			return s.StreamDictionarySnapshot(ctx, user, func(raw json.RawMessage) error { _, err := w.Write(append(raw, '\n')); return err })
		})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Candidates []struct {
				Code      string `json:"code"`
				Canonical string `json:"canonical_pinyin"`
				Word      string `json:"word"`
				Weight    int64  `json:"weight"`
			} `json:"candidates"`
		}
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out.Candidates
	}
	baseline := readCandidates(one.User.ID)
	if len(baseline) < 2 {
		t.Fatal(baseline)
	}
	selected := baseline[1]
	action := RankingAction{Code: selected.Canonical, Word: selected.Word, Mode: "pin", LinearStep: 1, TriggerCount: 2}
	first, err := s.RankCandidate(ctx, one.User.ID, 0, cfg, query, action)
	if err != nil || first.Changed || first.Selection.Count != 1 || len(first.Updates) != 0 {
		t.Fatalf("first trigger: %+v %v", first, err)
	}
	second, err := s.RankCandidate(ctx, one.User.ID, first.Revision, cfg, query, action)
	if err != nil || !second.Changed || second.Selection.Count != 0 || len(second.Updates) == 0 {
		t.Fatalf("second trigger: %+v %v", second, err)
	}
	after := readCandidates(one.User.ID)
	if after[0].Word != selected.Word {
		t.Fatalf("ranking not replayed: %+v", after)
	}
	isolated := readCandidates(two.User.ID)
	if isolated[0] != baseline[0] {
		t.Fatal("ranking leaked", isolated)
	}
	if _, err = s.RankCandidate(ctx, one.User.ID, first.Revision, cfg, query, action); err != errRevisionConflict {
		t.Fatal("stale rank", err)
	}
	entries, _, err := s.DictionaryEntries(ctx, one.User.ID, "pinyin", "", 0, 200)
	if err != nil || len(entries) != 0 {
		t.Fatal("base rank became personal insertion", entries, err)
	}
	changes, _, err := s.DictionaryChanges(ctx, one.User.ID, 0, 200)
	if err != nil || len(changes) != 2 || changes[0].Selection.Count != 1 || len(changes[1].Ranking) == 0 {
		t.Fatal("ranking changefeed", changes, err)
	}
	for _, e := range changes[1].Ranking {
		if e.UserInserted == nil || *e.UserInserted {
			t.Fatal("base entry ownership changed", e)
		}
	}
	// Rebuilding the derived overlay must retain frequency changes, not just personal additions.
	if _, err = s.pool.Exec(ctx, `TRUNCATE user_dictionary_overlay`); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt := readCandidates(one.User.ID)
	if rebuilt[0] != after[0] {
		t.Fatal("ranking rebuild mismatch", rebuilt)
	}
}
