package dataprovider

import (
	"context"
	"encoding/json"
	"errors"
)

// Code is a closed provider vocabulary. Authentication and domain errors belong
// to their respective boundaries, and callback errors pass through unchanged.
type Code string

const (
	InvalidRequest        Code = "invalid_request"
	NotFound              Code = "not_found"
	ScopeConflict         Code = "scope_conflict"
	RevisionRequired      Code = "revision_required"
	RevisionConflict      Code = "revision_conflict"
	AlreadyExists         Code = "already_exists"
	InvalidSnapshot       Code = "invalid_snapshot"
	PayloadTooLarge       Code = "payload_too_large"
	ResourceBinaryRefused Code = "resource_binary_refused"
	UnsupportedSchema     Code = "unsupported_schema"
	Unavailable           Code = "unavailable"
	StorageError          Code = "storage_error"
	CommitOutcomeUnknown  Code = "commit_outcome_unknown"
)

var messages = map[Code]string{
	InvalidRequest: "Invalid provider request", NotFound: "Scoped value not found",
	ScopeConflict: "Scope metadata differs", RevisionRequired: "Revision required",
	RevisionConflict: "Revision differs", AlreadyExists: "Import target is not empty",
	InvalidSnapshot: "Invalid provider state", PayloadTooLarge: "Provider byte bound exceeded",
	ResourceBinaryRefused: "Binary resource refused by domain", UnsupportedSchema: "Unsupported persistent schema",
	Unavailable: "Provider unavailable", StorageError: "Provider storage failure",
	CommitOutcomeUnknown: "Commit outcome unknown; read and reconcile before another write",
}

// Error exposes only a closed code and fixed safe message. Storage diagnostics
// must be retained separately by the server, never attached as an exposed cause.
type Error struct {
	code    Code
	context error
}

func (e *Error) Code() Code {
	if _, ok := messages[e.code]; !ok {
		return StorageError
	}
	return e.code
}
func (e *Error) Message() string { return messages[e.Code()] }
func (e *Error) Error() string   { return string(e.Code()) + ": " + e.Message() }
func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Code    Code   `json:"code"`
		Message string `json:"message"`
	}{e.Code(), e.Message()})
}
func (e *Error) Unwrap() error        { return e.context }
func (e *Error) Is(target error) bool { x, ok := target.(*Error); return ok && x.Code() == e.Code() }

// NewError returns storage_error for an unknown code, keeping the vocabulary
// closed. It deliberately takes no raw message or implementation error.
func NewError(code Code) *Error {
	if _, ok := messages[code]; !ok {
		code = StorageError
	}
	return &Error{code: code}
}

// ContextError preserves only the standard context sentinel, never arbitrary
// error text. It is an unavailable refusal and remains errors.Is recognizable.
func ContextError(err error) *Error {
	e := NewError(Unavailable)
	if errors.Is(err, context.Canceled) {
		e.context = context.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		e.context = context.DeadlineExceeded
	}
	return e
}
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code(), true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Unavailable, true
	}
	return "", false
}
