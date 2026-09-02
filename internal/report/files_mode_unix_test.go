//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package report

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileStoreCreatesParentsWithExactModeUnderRestrictiveUmask(t *testing.T) {
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	store, root := newFileStore(t)
	if _, err := store.Write(context.Background(), filepath.Join("one", "two", "report.html"), []byte("body")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "one"), filepath.Join(root, "one", "two")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o750 {
			t.Fatalf("directory %s mode = %04o, want 0750", path, got)
		}
	}
}
