//go:build !linux && !darwin

package tty

import "errors"

// Cbreak is not supported on this platform.
func Cbreak(fd int) (restore func(), err error) {
	return nil, errors.New("cbreak mode is not supported on this platform")
}
