//go:build linux

package evidence

import "golang.org/x/sys/unix"

func controllerEventRenameNewAt(directoryFD int, oldName, newName string) error {
	return unix.Renameat2(directoryFD, oldName, directoryFD, newName, unix.RENAME_NOREPLACE)
}
