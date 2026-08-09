package fsutil

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"
)

func TestRenameWithTransientRetryRetriesOnlyWindowsPermissionFailures(t *testing.T) {
	permissionErr := &fs.PathError{Op: "rename", Path: "old", Err: fs.ErrPermission}
	var calls [][2]string
	var delays []time.Duration
	rename := func(oldPath, newPath string) error {
		calls = append(calls, [2]string{oldPath, newPath})
		if len(calls) < 3 {
			return permissionErr
		}
		return nil
	}
	err := renameWithTransientRetry("windows", rename, func(delay time.Duration) {
		delays = append(delays, delay)
	}, "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if want := [][2]string{{"old", "new"}, {"old", "new"}, {"old", "new"}}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("rename calls = %v, want %v", calls, want)
	}
	if want := []time.Duration{25 * time.Millisecond, 50 * time.Millisecond}; !reflect.DeepEqual(delays, want) {
		t.Fatalf("retry delays = %v, want %v", delays, want)
	}
}

func TestRenameWithTransientRetryDoesNotRetryOtherFailures(t *testing.T) {
	tests := []struct {
		name string
		goos string
		err  error
	}{
		{name: "non-windows permission", goos: "linux", err: fs.ErrPermission},
		{name: "windows non-permission", goos: "windows", err: fs.ErrExist},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			got := renameWithTransientRetry(test.goos, func(string, string) error {
				calls++
				return test.err
			}, func(time.Duration) {
				t.Fatal("unexpected retry delay")
			}, "old", "new")
			if !errors.Is(got, test.err) || calls != 1 {
				t.Fatalf("error = %v calls = %d, want %v and one call", got, calls, test.err)
			}
		})
	}
}
