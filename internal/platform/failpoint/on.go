//go:build failpoint

// Package failpoint injects faults at named points in failure tests (ADR 0017).
package failpoint

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	once    sync.Once
	actions map[string]string
)

// Hit performs the action configured for name in FAILPOINT, if any:
// "exit" ends the process with code 137 (no shutdown hooks, like kill -9),
// "panic" panics, "sleep:<duration>" blocks.
func Hit(name string) {
	once.Do(func() { actions = parse(os.Getenv("FAILPOINT")) })
	action, ok := actions[name]
	if !ok {
		return
	}
	switch {
	case action == "exit":
		fmt.Fprintf(os.Stderr, "failpoint %s: exit\n", name)
		os.Exit(137)
	case action == "panic":
		panic("failpoint " + name)
	case strings.HasPrefix(action, "sleep:"):
		if d, err := time.ParseDuration(strings.TrimPrefix(action, "sleep:")); err == nil {
			time.Sleep(d)
		}
	}
}

func parse(spec string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		name, action, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && name != "" {
			out[name] = action
		}
	}
	return out
}
