//go:build !failpoint

// Package failpoint injects faults at named points in failure tests. Without
// the failpoint build tag every call is a no-op (ADR 0017).
package failpoint

// Hit does nothing in production builds.
func Hit(string) {}
