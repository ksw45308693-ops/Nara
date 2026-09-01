package app

import (
	"context"
	"errors"
	"sync"
)

// asyncCollectionTrigger owns manually requested collection work for the
// lifetime of the service, not the lifetime of the HTTP request that queued it.
// Repeated requests are coalesced while a collection is already queued/running.
type asyncCollectionTrigger struct {
	ctx context.Context
	run CollectionRunner

	wake chan struct{}
	mu   sync.Mutex
	busy bool
}

func newAsyncCollectionTrigger(ctx context.Context, run CollectionRunner) (*asyncCollectionTrigger, error) {
	if ctx == nil || run == nil {
		return nil, errors.New("service context and collection runner are required")
	}
	trigger := &asyncCollectionTrigger{
		ctx:  ctx,
		run:  run,
		wake: make(chan struct{}, 1),
	}
	go trigger.loop()
	return trigger, nil
}

func (t *asyncCollectionTrigger) Trigger() error {
	if t == nil || t.ctx == nil || t.run == nil {
		return errors.New("collection trigger is unavailable")
	}
	if err := t.ctx.Err(); err != nil {
		return err
	}

	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return nil
	}
	t.busy = true
	t.mu.Unlock()

	select {
	case t.wake <- struct{}{}:
		return nil
	case <-t.ctx.Done():
		t.mu.Lock()
		t.busy = false
		t.mu.Unlock()
		return t.ctx.Err()
	}
}

func (t *asyncCollectionTrigger) loop() {
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.wake:
			_, _ = t.run(t.ctx)
			t.mu.Lock()
			t.busy = false
			t.mu.Unlock()
		}
	}
}
