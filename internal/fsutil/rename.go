package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"
)

const windowsRenameRetryLimit = 8

// RenameWithTransientRetry preserves os.Rename semantics while tolerating the
// short-lived sharing violation Windows scanners can introduce after a file or
// directory handle has been closed. Other platforms and non-permission errors
// are attempted exactly once.
func RenameWithTransientRetry(oldPath, newPath string) error {
	return renameWithTransientRetry(runtime.GOOS, os.Rename, time.Sleep, oldPath, newPath)
}

func renameWithTransientRetry(
	goos string,
	rename func(string, string) error,
	sleep func(time.Duration),
	oldPath string,
	newPath string,
) error {
	err := rename(oldPath, newPath)
	if err == nil || goos != "windows" || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	for attempt := 1; attempt <= windowsRenameRetryLimit; attempt++ {
		sleep(time.Duration(attempt) * 25 * time.Millisecond)
		err = rename(oldPath, newPath)
		if err == nil || !errors.Is(err, fs.ErrPermission) {
			return err
		}
	}
	return err
}
