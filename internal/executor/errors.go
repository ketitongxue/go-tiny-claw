package executor

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidWorkDir    = errors.New("invalid execution working directory")
	ErrInvalidPolicy     = errors.New("invalid sandbox policy")
	ErrDockerUnavailable = errors.New("Docker sandbox unavailable")
)

// InfrastructureError distinguishes startup, isolation and cancellation errors
// from an ordinary nonzero command exit code. Unwrap preserves errors.Is/As,
// including context.Canceled and context.DeadlineExceeded.
type InfrastructureError struct {
	Op  string
	Err error
}

func (e *InfrastructureError) Error() string {
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *InfrastructureError) Unwrap() error { return e.Err }
