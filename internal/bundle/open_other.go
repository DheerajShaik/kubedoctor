//go:build !linux && !darwin && !windows

package bundle

import "os"

func openBundleFile(path string) (*os.File, error) {
	return os.Open(path)
}
