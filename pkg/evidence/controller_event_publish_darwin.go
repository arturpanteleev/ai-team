//go:build darwin

package evidence

import "golang.org/x/sys/unix"

func controllerEventRenameNewAt(directoryFD int, oldName, newName string) error {
	return unix.RenameatxNp(directoryFD, oldName, directoryFD, newName, unix.RENAME_EXCL)
}
