package tools

import (
	"os"
	"testing"
	"time"
)

func testStoreDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Cleanup(func() {
		removeTempDirWithRetry(t, dir)
	})
	return dir
}

func removeTempDirWithRetry(t *testing.T, dir string) {
	t.Helper()

	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.RemoveAll(dir)
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	t.Logf("pre-clean temp dir %q: %v", dir, err)
}
