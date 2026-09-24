package health

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadiness(t *testing.T) {
	ok := Check{Name: "postgres", Probe: func(context.Context) error { return nil }}
	bad := Check{Name: "sqs", Probe: func(context.Context) error { return errors.New("down") }}

	if err := NewReadiness(ok).Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := NewReadiness(ok, bad).Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sqs") {
		t.Fatalf("error = %v", err)
	}
	r := NewReadiness(ok)
	r.SetDraining()
	if err := r.Ready(context.Background()); !errors.Is(err, ErrDraining) {
		t.Fatalf("draining error = %v", err)
	}
}
