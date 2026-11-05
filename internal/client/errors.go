// Package client implements the frozen swarma-board v1 HTTP transport.
package client

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

// Error is a safe boundary error. Server text and transport errors are never
// included: either can contain credentials, request bodies or internal paths.
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	RequestID string         `json:"-"`
	Status    int            `json:"-"`
	Local     bool           `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) MarshalJSON() ([]byte, error) {
	type wire Error
	return json.Marshal(struct {
		Error     wire   `json:"error"`
		RequestID string `json:"request_id"`
	}{wire(*e), e.RequestID})
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func RequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "client-request"
	}
	return hex.EncodeToString(b[:])
}
func LocalError(code, message string) *Error {
	return &Error{Code: code, Message: message, Details: map[string]any{}, RequestID: RequestID(), Local: true}
}
func failure(code, message, id string, status int) *Error {
	if !safeID.MatchString(id) {
		id = RequestID()
	}
	return &Error{Code: code, Message: message, Details: map[string]any{}, RequestID: id, Status: status}
}
func transportError(write bool) *Error {
	msg := "request failed; try a read to check service availability"
	if write {
		msg = "write outcome is unknown; read and reconcile before another write"
	}
	return failure("transport_error", msg, "", 0)
}
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	e, ok := err.(*Error)
	if !ok {
		return 1
	}
	if e.Local {
		return 2
	}
	if e.Status == 401 || e.Status == 403 || e.Code == "unauthorized" || e.Code == "forbidden" {
		return 3
	}
	if e.Status == 409 || e.Status == 412 || e.Code == "already_exists" || e.Code == "dependency_cycle" || e.Code == "dependency_conflict" || e.Code == "revision_conflict" {
		return 4
	}
	return 1
}
func safeMessage(code string) string {
	switch code {
	case "revision_conflict":
		return "revision conflict; read and reconcile before another write"
	case "unauthorized":
		return "authentication refused"
	case "forbidden":
		return "project access refused"
	default:
		return fmt.Sprintf("server refused request (%s)", code)
	}
}

func protocolError(write bool) *Error {
	if write {
		return transportError(true)
	}
	return LocalError("invalid_request", "server response does not match the frozen contract")
}
