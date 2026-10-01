package account

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// Two services on separate pools stand in for two replicas: the second waits while the first holds the lock, gets ErrLockBusy once its wait runs out, takes the lock as soon as the first releases it, and a waiter whose request is cancelled stops at once.
func TestWaitLockAcrossPools(t *testing.T) {
	first := &Service{store: testStore(t)}
	other, err := Open(context.Background(), os.Getenv("MSIME_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	second := &Service{store: other}
	ctx := context.Background()
	name := "test:" + t.Name() + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)

	release, err := first.WaitLock(ctx, name, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err = second.WaitLock(ctx, name, 300*time.Millisecond); !errors.Is(err, ErrLockBusy) {
		t.Fatal("second replica took a held lock", err)
	}
	if waited := time.Since(started); waited < 300*time.Millisecond {
		t.Fatal("gave up before the wait ran out", waited)
	}
	// Waiting must not keep a connection: only the holder's connection is checked out.
	if acquired := other.pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatal("a waiter kept a pooled connection", acquired)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = second.WaitLock(cancelled, name, 5*time.Second); err == nil {
		t.Fatal("a cancelled waiter took the lock")
	}

	got := make(chan error, 1)
	go func() {
		release, err := second.WaitLock(ctx, name, 5*time.Second)
		if err == nil {
			release()
		}
		got <- err
	}()
	time.Sleep(100 * time.Millisecond)
	release()
	if err = <-got; err != nil {
		t.Fatal("waiter did not get the released lock", err)
	}
	// Released on both sides: the first takes it again without waiting, and the pool holds no connection afterwards.
	release, err = first.WaitLock(ctx, name, 0)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if acquired := first.store.pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatal("release kept the connection", acquired)
	}
}
