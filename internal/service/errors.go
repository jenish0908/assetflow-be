package service

import (
	"errors"
	"fmt"
)

// Error kinds. Callers check them with errors.Is; the HTTP layer maps each
// kind to a status code.
var (
	ErrInvalid         = errors.New("invalid input")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrUnauthenticated = errors.New("unauthenticated")
)

// Error is an error whose message is safe to show to API clients.
// Kind is one of the sentinels above, so errors.Is(err, ErrNotFound) works.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

// NewError builds a client-facing error of the given kind.
func NewError(kind error, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// notFound turns a store-level ErrNotFound into a client-facing message and
// passes any other error through unchanged.
func notFound(err error, what string, id int64) error {
	if errors.Is(err, ErrNotFound) {
		return NewError(ErrNotFound, "%s %d not found", what, id)
	}
	return err
}
