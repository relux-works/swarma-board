package testadapter

import (
	"context"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

const maxCredentialBytes = 4096

type credential struct{ bearer string }

func (c *credential) Bearer(ctx context.Context, _ contract.Projection) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return c.bearer, nil
}

// CredentialFromFile consumes and closes exactly the explicitly supplied file.
// It never opens a path, searches the environment, logs or persists the value.
// The 4096-byte bound includes the optional single LF terminator.
func CredentialFromFile(ctx context.Context, file *os.File) (contract.CredentialSource, error) {
	if file == nil {
		return nil, credentialError()
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil {
		return nil, credentialError()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > maxCredentialBytes || !utf8.Valid(data) {
		return nil, credentialError()
	}
	value := strings.TrimSuffix(string(data), "\n")
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, credentialError()
	}
	return &credential{bearer: value}, nil
}
func credentialError() error {
	return &contract.Error{Code: "invalid_request", Message: "invalid explicit synthetic credential fd"}
}

var _ contract.CredentialSource = (*credential)(nil)
