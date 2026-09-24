package platform

import "github.com/google/uuid"

// UUIDv7 generates time-ordered identifiers, which keep B-tree inserts local.
type UUIDv7 struct{}

func NewUUIDv7() UUIDv7 { return UUIDv7{} }

func (UUIDv7) New() string { return uuid.Must(uuid.NewV7()).String() }
