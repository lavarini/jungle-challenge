// Package platform holds process-level infrastructure shared by adapters.
package platform

import "time"

type SystemClock struct{}

func NewSystemClock() SystemClock { return SystemClock{} }

func (SystemClock) Now() time.Time { return time.Now().UTC() }
