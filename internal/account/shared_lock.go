package account

import (
	"context"
	"errors"
	"time"
)

// ErrLockBusy is returned by WaitLock when another request, on this replica or another, still holds the lock after the wait.
var ErrLockBusy = errors.New("lock held by another request")

// WaitLock serialises a critical section across requests and replicas under name, for work that cannot be expressed as one database transaction (for example a read-modify-write on GitHub). It holds a session advisory lock on a pooled connection until the returned release is called, so callers keep the section short and serialise their own process first: the pool is small and every holder occupies one connection. While another holder has the lock it polls with pg_try_advisory_lock and gives the connection back between attempts, so waiting costs no connection; after wait it returns ErrLockBusy.
func (a *Service) WaitLock(ctx context.Context, name string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	delay := 50 * time.Millisecond
	for {
		release, err := a.tryLock(ctx, name)
		if err != nil || release != nil {
			return release, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrLockBusy
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

// tryLock takes the lock once; a nil release with a nil error means another session holds it. The key is a 64-bit hash of a prefixed name, the same form NotifyOnce uses, so it cannot meet the two-key crash issue locks or the migration lock.
func (a *Service) tryLock(ctx context.Context, name string) (func(), error) {
	conn, err := a.store.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('shared-lock:'||$1,0))`, name).Scan(&locked); err != nil || !locked {
		if err != nil {
			// The statement may have taken the lock before the error reached us; closing the session is the only way to be sure it is gone.
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Conn().Close(closeCtx)
			cancel()
		}
		conn.Release()
		return nil, err
	}
	return func() {
		// The request context may already be done; unlock on a fresh one, and drop the connection rather than return it to the pool still holding the lock.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended('shared-lock:'||$1,0))`, name); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
		conn.Release()
	}, nil
}
