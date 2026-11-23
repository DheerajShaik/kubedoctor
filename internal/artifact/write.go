// Package artifact publishes operator-requested local files.
package artifact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// WriteNew publishes a fully rendered file without overwriting an existing name.
// A hard link in the same directory makes the existence check and publication
// a single filesystem operation, including when concurrent writers race.
func WriteNew(path string, render func(io.Writer) error) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".kubedoctor-*")
	if err != nil {
		return errors.New("could not create a private temporary file in the output directory")
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := render(temp); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return errors.New("could not flush the output file")
	}
	if err := temp.Close(); err != nil {
		return errors.New("could not close the output file")
	}
	if err := os.Link(temp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("output already exists; choose a new path")
		}
		return errors.New("could not publish the output file; the destination must support hard links")
	}
	return nil
}
