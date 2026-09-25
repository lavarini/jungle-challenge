package main

import (
	"math/rand/v2"
	"sync"
	"testing"
)

func TestWithConflictingPayloadChangesOnlyMoney(t *testing.T) {
	original := submitRequest{
		ProviderID: "provider-a", ExternalTransactionID: "ext-1", PlayerID: "player-1",
		WalletID: "wallet-1", RoundID: "round-1", GameID: "game-1", Kind: "BET",
		Money: money{Amount: betAmount, Currency: currency},
	}

	conflicting := withConflictingPayload(original)

	if conflicting.Money.Amount == original.Money.Amount {
		t.Fatalf("conflicting payload has the same amount as the original: %s", conflicting.Money.Amount)
	}
	if conflicting.Money.Amount != conflictAmount {
		t.Fatalf("conflicting amount = %s, want %s", conflicting.Money.Amount, conflictAmount)
	}
	conflicting.Money = original.Money // now compare the rest field by field
	if conflicting != original {
		t.Fatalf("withConflictingPayload changed more than Money: got %+v, want %+v", conflicting, original)
	}
}

func TestRecentOpsPickEmptyReturnsFalse(t *testing.T) {
	r := newRecentOps(4)
	rng := rand.New(rand.NewPCG(1, 1))
	if _, ok := r.pick(rng); ok {
		t.Fatal("pick on an empty pool should return false")
	}
}

func TestRecentOpsPickReturnsAddedItem(t *testing.T) {
	r := newRecentOps(4)
	op := recentOp{idemKey: "provider-a:ext-1", body: submitRequest{ExternalTransactionID: "ext-1"}}
	r.add(op)

	rng := rand.New(rand.NewPCG(1, 1))
	got, ok := r.pick(rng)
	if !ok {
		t.Fatal("pick after one add should return true")
	}
	if got != op {
		t.Fatalf("pick = %+v, want %+v", got, op)
	}
}

func TestRecentOpsWrapsAtCapacity(t *testing.T) {
	r := newRecentOps(2)
	a := recentOp{idemKey: "a"}
	b := recentOp{idemKey: "b"}
	c := recentOp{idemKey: "c"}
	r.add(a)
	r.add(b)
	r.add(c) // overwrites a; buffer should now only ever yield b or c

	rng := rand.New(rand.NewPCG(7, 7))
	for i := 0; i < 200; i++ {
		got, ok := r.pick(rng)
		if !ok {
			t.Fatal("pick should return true once items were added")
		}
		if got == a {
			t.Fatalf("pick returned the overwritten item %+v after capacity wrapped", a)
		}
		if got != b && got != c {
			t.Fatalf("pick returned unexpected item %+v", got)
		}
	}
}

// TestRecentOpsConcurrentAccess exercises add/pick from many goroutines at once; it is meant to
// be run with -race, so a missing or misplaced lock turns into a race detector failure rather
// than a flaky assertion.
func TestRecentOpsConcurrentAccess(t *testing.T) {
	r := newRecentOps(16)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(id)+1, uint64(id)+1))
			for j := 0; j < 100; j++ {
				r.add(recentOp{idemKey: "worker"})
				r.pick(rng)
			}
		}(i)
	}
	wg.Wait()
}
