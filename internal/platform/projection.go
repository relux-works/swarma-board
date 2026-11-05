// Package platform contains explicit synthetic projection composition only.
// Production platform ownership and discovery remain an external hold.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

// Projection reads a bounded fixture with exactly three nonduplicated fields.
// It never discovers files or configuration from cwd, home or environment.
func Projection(r io.Reader) (contract.ProjectionSource, error) {
	b, err := io.ReadAll(io.LimitReader(r, 16385))
	if err != nil || len(b) > 16384 || !utf8.Valid(b) {
		return nil, invalid()
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, invalid()
	}
	values := map[string]string{}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return nil, invalid()
		}
		key, ok := tok.(string)
		if !ok {
			return nil, invalid()
		}
		if _, exists := values[key]; exists {
			return nil, invalid()
		}
		if key != "endpoint" && key != "project_id" && key != "account_id" {
			return nil, invalid()
		}
		var value string
		if d.Decode(&value) != nil || value == "" {
			return nil, invalid()
		}
		values[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, invalid()
	}
	if _, err = d.Token(); err != io.EOF || len(values) != 3 {
		return nil, invalid()
	}
	p := contract.Projection{Endpoint: values["endpoint"], ProjectID: values["project_id"], AccountID: values["account_id"]}
	if err := client.ValidateProjection(p); err != nil {
		return nil, err
	}
	return testadapter.Projection{Value: p}, nil
}
func invalid() error {
	return client.LocalError("invalid_request", "invalid synthetic projection fixture")
}

// Synthetic loads only the explicitly named projection and explicitly supplied
// fd, using the baseline's one-shot/close/bounded credential adapter.
func Synthetic(ctx context.Context, path string, fd *os.File) (contract.ProjectionSource, contract.CredentialSource, error) {
	if fd == nil {
		return nil, nil, invalid()
	}
	defer fd.Close()
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, invalid()
	}
	defer f.Close()
	p, err := Projection(f)
	if err != nil {
		return nil, nil, err
	}
	c, err := testadapter.CredentialFromFile(ctx, fd)
	if err != nil {
		return nil, nil, client.LocalError("invalid_request", "invalid synthetic credential fd")
	}
	return p, c, nil
}
