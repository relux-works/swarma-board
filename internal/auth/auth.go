// Package auth provides synthetic Bearer authorization and an injected protected
// peer-authority adapter. Synthetic fixture changes are adopted only at restart.
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Account struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
}
type Token struct {
	SHA256    string     `json:"sha256"`
	AccountID string     `json:"account_id"`
	Revoked   bool       `json:"revoked"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type Grant struct {
	AccountID string `json:"account_id"`
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}
type fixture struct {
	SchemaVersion int       `json:"schema_version"`
	Synthetic     bool      `json:"synthetic"`
	Projects      []Project `json:"projects"`
	Accounts      []Account `json:"accounts"`
	Tokens        []Token   `json:"tokens"`
	Grants        []Grant   `json:"grants"`
}
type Authorizer struct {
	projects map[string]contract.Project
	accounts map[string]Account
	tokens   map[string]Token
	grants   map[string]map[string]string
	now      func() time.Time
}

func refuse(code string) error {
	return &contract.Error{Code: code, Message: strings.ReplaceAll(code, "_", " ")}
}
func rank(role string) int {
	switch role {
	case "viewer":
		return 1
	case "editor":
		return 2
	case "admin":
		return 3
	}
	return 0
}
func validAccount(id string) bool {
	if id == "" || len(id) > 4096 || !utf8.ValidString(id) {
		return false
	}
	for _, c := range id {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}
func validID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i, c := range id {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

// New validates the entire bounded fixture before returning any authority.
func New(r io.Reader, now func() time.Time) (*Authorizer, error) {
	if r == nil || now == nil {
		return nil, refuse("invalid_request")
	}
	data, e := io.ReadAll(io.LimitReader(r, (8<<20)+1))
	if e != nil || len(data) > 8<<20 || !utf8.Valid(data) {
		return nil, refuse("invalid_request")
	}
	if e = validateFixtureJSON(data); e != nil {
		return nil, refuse("invalid_request")
	}
	var f fixture
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&f) != nil || f.SchemaVersion != 1 || !f.Synthetic {
		return nil, refuse("invalid_request")
	}
	a := &Authorizer{map[string]contract.Project{}, map[string]Account{}, map[string]Token{}, map[string]map[string]string{}, now}
	for _, p := range f.Projects {
		if !validID(p.ID) {
			return nil, refuse("invalid_request")
		}
		if _, ok := a.projects[p.ID]; ok {
			return nil, refuse("invalid_request")
		}
		a.projects[p.ID] = contract.Project{ID: p.ID, Name: p.Name, Description: p.Description}
	}
	for _, v := range f.Accounts {
		if !validAccount(v.ID) {
			return nil, refuse("invalid_request")
		}
		if _, ok := a.accounts[v.ID]; ok {
			return nil, refuse("invalid_request")
		}
		a.accounts[v.ID] = v
	}
	for _, t := range f.Tokens {
		hash, e := hex.DecodeString(t.SHA256)
		if e != nil || len(hash) != 32 || strings.ToLower(t.SHA256) != t.SHA256 {
			return nil, refuse("invalid_request")
		}
		if _, ok := a.accounts[t.AccountID]; !ok {
			return nil, refuse("invalid_request")
		}
		if _, ok := a.tokens[t.SHA256]; ok {
			return nil, refuse("invalid_request")
		}
		if t.ExpiresAt != nil {
			_, offset := t.ExpiresAt.Zone()
			if offset != 0 {
				return nil, refuse("invalid_request")
			}
		}
		a.tokens[t.SHA256] = t
	}
	for _, g := range f.Grants {
		if _, ok := a.accounts[g.AccountID]; !ok {
			return nil, refuse("invalid_request")
		}
		if _, ok := a.projects[g.ProjectID]; !ok || rank(g.Role) == 0 {
			return nil, refuse("invalid_request")
		}
		if a.grants[g.AccountID] == nil {
			a.grants[g.AccountID] = map[string]string{}
		}
		if a.grants[g.AccountID][g.ProjectID] != "" {
			return nil, refuse("invalid_request")
		}
		a.grants[g.AccountID][g.ProjectID] = g.Role
	}
	return a, nil
}
func (a *Authorizer) Authenticate(ctx context.Context, bearer string) (contract.Principal, error) {
	if e := ctx.Err(); e != nil {
		return contract.Principal{}, e
	}
	if bearer == "" || len(bearer) > 4096 || !utf8.ValidString(bearer) || strings.ContainsAny(bearer, " \t\r\n") {
		return contract.Principal{}, refuse("unauthorized")
	}
	hash := sha256.Sum256([]byte(bearer))
	t, ok := a.tokens[hex.EncodeToString(hash[:])]
	account := a.accounts[t.AccountID]
	if !ok || t.Revoked || account.Disabled || (t.ExpiresAt != nil && !a.now().Before(*t.ExpiresAt)) {
		return contract.Principal{}, refuse("unauthorized")
	}
	return contract.Principal{AccountID: t.AccountID}, nil
}
func (a *Authorizer) Require(ctx context.Context, p contract.Principal, project, role string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	v, ok := a.accounts[p.AccountID]
	if !ok || v.Disabled {
		return refuse("unauthorized")
	}
	if rank(role) == 0 || rank(a.grants[p.AccountID][project]) < rank(role) {
		return refuse("forbidden")
	}
	return nil
}
func (a *Authorizer) Projects(ctx context.Context, p contract.Principal) ([]contract.Project, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	v, ok := a.accounts[p.AccountID]
	if !ok || v.Disabled {
		return nil, refuse("unauthorized")
	}
	out := []contract.Project{}
	for id := range a.grants[p.AccountID] {
		out = append(out, a.projects[id])
	}
	sortProjects(out)
	return out, nil
}
func (a *Authorizer) ProvisionProjects() []contract.Project {
	out := []contract.Project{}
	for _, p := range a.projects {
		out = append(out, p)
	}
	sortProjects(out)
	return out
}
func (a *Authorizer) Ready(ctx context.Context) error {
	if a == nil || a.now == nil {
		return refuse("unavailable")
	}
	return ctx.Err()
}

// ValidateSyntheticListen requires an explicit literal loopback, never localhost/DNS.
func ValidateSyntheticListen(addr string) error {
	host, port, e := net.SplitHostPort(addr)
	if e != nil {
		return refuse("invalid_request")
	}
	ip := net.ParseIP(host)
	n, e := strconv.Atoi(port)
	if e != nil || n < 0 || n > 65535 || ip == nil || !ip.IsLoopback() {
		return refuse("invalid_request")
	}
	return nil
}

var _ contract.Authorizer = (*Authorizer)(nil)
