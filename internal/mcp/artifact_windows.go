package mcp

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Anchor the basename to the opened directory. Reparse points, including
// symlinks and junctions, are excluded from artifact imports.
func openArtifactFile(directory *os.File, name string) (*os.File, error) {
	path, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	var attributes windows.OBJECT_ATTRIBUTES
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	attributes.RootDirectory = windows.Handle(directory.Fd())
	attributes.ObjectName = path
	attributes.Attributes = windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE

	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, &attributes, &status, nil,
		windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("bundle import is not a regular file")
	}
	return os.NewFile(uintptr(handle), name), nil
}
