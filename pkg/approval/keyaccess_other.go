//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package approval

// checkKeyFileAccess на платформах без POSIX-режима файла ограничивается
// проверками уровня safeio: там права на файл не выражают ту же модель
// доступа, и отказ по mode давал бы ложные срабатывания.
func checkKeyFileAccess(string) error { return nil }
