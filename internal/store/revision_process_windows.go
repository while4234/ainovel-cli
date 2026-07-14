//go:build windows

package store

import "syscall"

func revisionProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}
	// Access denied means the process exists but cannot be queried.
	return err == syscall.ERROR_ACCESS_DENIED
}
