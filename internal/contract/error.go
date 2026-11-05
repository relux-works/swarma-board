package contract

// Error returns only the caller-supplied safe message, never Details. Adapters
// own status/exit mapping and must sanitize any facts placed in Details.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}
