package main

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
)

// cacheSyncGate keeps /readyz red until the caches have synced, so a rollout
// does not report healthy while the operator is still blind to existing bots.
// The manager starts non-leader runnables after the cache sync and before
// leader election, which is why this does not need the lease.
type cacheSyncGate struct {
	synced atomic.Bool
}

func (g *cacheSyncGate) Start(ctx context.Context) error {
	g.synced.Store(true)
	<-ctx.Done()
	return nil
}

func (g *cacheSyncGate) NeedLeaderElection() bool { return false }

func (g *cacheSyncGate) Check(*http.Request) error {
	if !g.synced.Load() {
		return errors.New("caches have not synced")
	}
	return nil
}
