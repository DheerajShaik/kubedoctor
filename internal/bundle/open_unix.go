//go:build linux || darwin

package bundle

import (
	"os"
	"syscall"
)

func openBundleFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
