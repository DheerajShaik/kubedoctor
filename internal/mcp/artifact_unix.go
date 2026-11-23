//go:build unix

package mcp

import (
	"os"

	"golang.org/x/sys/unix"
)

// Open relative to a directory handle. NONBLOCK keeps a concurrent FIFO
// substitution from blocking before the caller can verify the file type.
func openArtifactFile(directory *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
