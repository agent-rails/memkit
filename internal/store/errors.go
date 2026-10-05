package store

import "errors"

// ErrNotFound is returned when a memory does not exist for the given tenant.
var ErrNotFound = errors.New("memory not found")

// ErrNotActive is returned by Supersede when the memory exists but has already
// been superseded, so a concurrent change is never silently overwritten.
var ErrNotActive = errors.New("memory is not active")
