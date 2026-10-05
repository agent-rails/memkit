package api

import (
	"context"
	"sync"
)

// keyedMutex serializes work per key without keeping a lock object for every key
// ever seen: an entry exists only while someone holds or waits for it. A waiter
// stops waiting when its context ends.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedEntry
}

type keyedEntry struct {
	sem  chan struct{}
	refs int
}

func (k *keyedMutex) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedEntry{}
	}
	e := k.locks[key]
	if e == nil {
		e = &keyedEntry{sem: make(chan struct{}, 1)}
		k.locks[key] = e
	}
	e.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
	return func() {
		<-e.sem
		release()
	}, nil
}
