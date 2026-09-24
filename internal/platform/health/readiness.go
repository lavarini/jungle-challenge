// Package health aggregates dependency probes for the readiness endpoint.
package health

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

var ErrDraining = errors.New("shutting down")

type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

type Readiness struct {
	checks   []Check
	draining atomic.Bool
}

func NewReadiness(checks ...Check) *Readiness { return &Readiness{checks: checks} }

// SetDraining makes the process report not-ready before it stops accepting work.
func (r *Readiness) SetDraining() { r.draining.Store(true) }

func (r *Readiness) Ready(ctx context.Context) error {
	if r.draining.Load() {
		return ErrDraining
	}
	var errs []error
	for _, c := range r.checks {
		if err := c.Probe(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}
