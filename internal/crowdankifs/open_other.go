//go:build !linux && !windows

package crowdankifs

import (
	"errors"
	"os"
)

func openRegular(string) (*os.File, error) {
	return nil, errors.ErrUnsupported
}
