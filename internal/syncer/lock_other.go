//go:build !linux && !darwin

package syncer

import (
	"errors"
	"os"
)

func lockDirectory(string) (*os.File, error) {
	return nil, errors.New("locking_unsupported")
}
