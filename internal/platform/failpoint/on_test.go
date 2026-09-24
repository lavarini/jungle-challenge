//go:build failpoint

package failpoint

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	got := parse("consumer.after_commit=exit, outbox.after_publish=sleep:10ms,broken")
	if got["consumer.after_commit"] != "exit" || got["outbox.after_publish"] != "sleep:10ms" || len(got) != 2 {
		t.Fatalf("parse = %v", got)
	}
}

func TestSleepAndPanic(t *testing.T) {
	once.Do(func() {})
	actions = map[string]string{"a": "sleep:20ms", "b": "panic"}
	start := time.Now()
	Hit("a")
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("sleep action did not block")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("panic action did not panic")
		}
	}()
	Hit("b")
}
