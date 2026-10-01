package account

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func skinJobID(c byte) string { return strings.Repeat(string(c), 48) }

// The caps hold across callers sharing the table, a cancelled job keeps its slot until its worker has finished, and a stale job is failed for good.
func TestSkinJobStoreCapsCancelAndStaleness(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `TRUNCATE skin_jobs`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []byte{'a', 'b'} {
		if _, err := a.CreateSkinJob(ctx, skinJobID(c), "owner-1", 2, 3, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.CreateSkinJob(ctx, skinJobID('c'), "owner-1", 2, 3, time.Minute); !errors.Is(err, ErrSkinJobsBusy) {
		t.Fatal("per-owner cap", err)
	}
	if _, err := a.CreateSkinJob(ctx, skinJobID('c'), "owner-2", 2, 3, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateSkinJob(ctx, skinJobID('d'), "owner-2", 2, 3, time.Minute); !errors.Is(err, ErrSkinJobsBusy) {
		t.Fatal("global cap", err)
	}
	if err := a.DeleteSkinJob(ctx, skinJobID('a'), "owner-2"); !errors.Is(err, ErrSkinJobNotFound) {
		t.Fatal("cross-owner delete", err)
	}
	if err := a.DeleteSkinJob(ctx, skinJobID('a'), "owner-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetSkinJob(ctx, skinJobID('a'), "owner-1"); !errors.Is(err, ErrSkinJobNotFound) {
		t.Fatal("cancelled job visible", err)
	}
	if alive, err := a.SkinJobHeartbeat(ctx, skinJobID('a')); err != nil || alive {
		t.Fatal("cancelled worker told to continue", alive, err)
	}
	if _, err := a.CreateSkinJob(ctx, skinJobID('d'), "owner-2", 2, 3, time.Minute); !errors.Is(err, ErrSkinJobsBusy) {
		t.Fatal("cancelled job released its slot before its worker stopped", err)
	}
	if recorded, err := a.FinishSkinJob(ctx, skinJobID('a'), "failed", "cancelled", nil); err != nil || recorded {
		t.Fatal(recorded, err)
	}
	if _, err := a.CreateSkinJob(ctx, skinJobID('d'), "owner-2", 2, 3, time.Minute); err != nil {
		t.Fatal("slot not released", err)
	}

	if _, err := db.pool.Exec(ctx, `UPDATE skin_jobs SET heartbeat_at=now()-interval '1 minute' WHERE id=$1`, skinJobID('b')); err != nil {
		t.Fatal(err)
	}
	if job, err := a.GetSkinJob(ctx, skinJobID('b'), "owner-1"); err != nil || job.State != "failed" || job.Reason != "cancelled" || job.Artwork != nil {
		t.Fatal("stale job", job, err)
	}
	if alive, err := a.SkinJobHeartbeat(ctx, skinJobID('b')); err != nil || alive {
		t.Fatal("stale worker told to continue", alive, err)
	}
	if recorded, err := a.FinishSkinJob(ctx, skinJobID('b'), "succeeded", "", []byte(`{}`)); err != nil || recorded {
		t.Fatal("stale job revived", recorded, err)
	}

	if alive, err := a.SkinJobHeartbeat(ctx, skinJobID('c')); err != nil || !alive {
		t.Fatal(alive, err)
	}
	if recorded, err := a.FinishSkinJob(ctx, skinJobID('c'), "succeeded", "", []byte(`{"width":1}`)); err != nil || !recorded {
		t.Fatal(recorded, err)
	}
	if job, err := a.GetSkinJob(ctx, skinJobID('c'), "owner-2"); err != nil || job.State != "succeeded" || job.Reason != "" || string(job.Artwork) != `{"width":1}` {
		t.Fatal(job, err)
	}

	if _, err := db.pool.Exec(ctx, `UPDATE skin_jobs SET expires_at=now()-interval '1 second' WHERE id<>$1`, skinJobID('d')); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetSkinJob(ctx, skinJobID('c'), "owner-2"); !errors.Is(err, ErrSkinJobNotFound) {
		t.Fatal("expired job visible", err)
	}
	db.Prune(ctx)
	var ids []string
	rows, err := db.pool.Query(ctx, `SELECT id FROM skin_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil || len(ids) != 1 || ids[0] != skinJobID('d') {
		t.Fatal("prune", ids, rows.Err())
	}
}
