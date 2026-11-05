package platform

import (
	"context"
	"fmt"
	"github.com/relux-works/swarma-board/internal/contract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validFixture = `{"endpoint":"http://127.0.0.1:1234","project_id":"synthetic-board","account_id":"synthetic-account"}`

func TestStrictProjection(t *testing.T) {
	for _, input := range []string{`{}`, `null`, `[]`, validFixture + `{}`, strings.Replace(validFixture, `"endpoint":`, `"extra":"x","endpoint":`, 1), strings.Replace(validFixture, `"endpoint":`, `"endpoint":"https://evil.test","endpoint":`, 1), strings.Replace(validFixture, `"synthetic-account"`, `null`, 1), strings.Replace(validFixture, `"synthetic-board"`, `""`, 1), strings.Replace(validFixture, `127.0.0.1`, `localhost`, 1), strings.Repeat(" ", 16385) + validFixture, "\xff"} {
		if _, err := Projection(strings.NewReader(input)); err == nil {
			t.Errorf("accepted invalid fixture %.100q", input)
		}
	}
	p, err := Projection(strings.NewReader(validFixture))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Resolve(context.Background())
	if err != nil || got.ProjectID != "synthetic-board" || got.AccountID != "synthetic-account" {
		t.Fatal(got, err)
	}
}
func TestSyntheticConsumesAndClosesFD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projection.json")
	if err := os.WriteFile(path, []byte(validFixture), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"synthetic-memory-token\n", "", strings.Repeat("x", 4097), "token\nextra", "token\r\n"} {
		t.Run(fmt.Sprint(len(value)), func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "credential")
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString(value)
			f.Seek(0, 0)
			_, c, err := Synthetic(context.Background(), path, f)
			if _, readErr := f.Read(make([]byte, 1)); readErr == nil {
				t.Fatal("fd left open")
			}
			if value == "synthetic-memory-token\n" {
				if err != nil {
					t.Fatal(err)
				}
				token, err := c.Bearer(context.Background(), contract.Projection{})
				if err != nil || token != "synthetic-memory-token" {
					t.Fatal("bad credential")
				}
			} else if err == nil {
				t.Fatal("bad fd accepted")
			}
		})
	}
	f, err := os.CreateTemp(t.TempDir(), "credential")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Synthetic(context.Background(), path+"-missing", f); err == nil {
		t.Fatal("missing fixture accepted")
	}
	if _, err := f.Read(make([]byte, 1)); err == nil {
		t.Fatal("fd left open on error")
	}
}
