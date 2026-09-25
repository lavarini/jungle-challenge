package main

import (
	"math/rand/v2"
	"sync"
)

// recentOp is one "new" operation a worker submitted, kept around so -conflict can target it
// from another worker.
type recentOp struct {
	idemKey string
	body    submitRequest
}

// recentOps is a small fixed-size ring buffer of recently submitted new operations, shared
// across all workers so the -conflict path can pick a target that was not necessarily generated
// by the calling goroutine - a real concurrent collision on the same Idempotency-Key, not just a
// worker replaying itself (that is what -dup already does).
type recentOps struct {
	mu    sync.Mutex
	items []recentOp
	next  int
	count int
}

func newRecentOps(capacity int) *recentOps {
	return &recentOps{items: make([]recentOp, capacity)}
}

// add records op, overwriting the oldest entry once the buffer is full.
func (r *recentOps) add(op recentOp) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[r.next] = op
	r.next = (r.next + 1) % len(r.items)
	if r.count < len(r.items) {
		r.count++
	}
}

// pick returns a uniformly random recorded op and true, or the zero value and false if none has
// been recorded yet.
func (r *recentOps) pick(rng *rand.Rand) (recentOp, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return recentOp{}, false
	}
	return r.items[rng.IntN(r.count)], true
}

// withConflictingPayload returns a copy of body whose canonical payload differs from the
// original - a different bet amount - while keeping the same provider, wallet, player and
// external id. Submitting this under body's own Idempotency-Key is what makes the server compare
// hashes, find a mismatch and answer 409 IDEMPOTENCY_PAYLOAD_MISMATCH (docs/openapi.yaml) instead
// of replaying cleanly like -dup's exact resend does.
func withConflictingPayload(body submitRequest) submitRequest {
	conflicting := body
	conflicting.Money = money{Amount: conflictAmount, Currency: body.Money.Currency}
	return conflicting
}
