//go:build !linux || !amd64

package app

import (
	"errors"
	"os"
)

func renameCheckpointNoReplace(_ *os.File, _, _ string) error {
	return errors.New("checkpoint no-replace publication is qualified only on Linux amd64")
}
