package report

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileStoreRejectsInvalidRoots(t *testing.T) {
	temp := t.TempDir()
	file := filepath.Join(temp, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawParent := temp + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(temp)
	volumeRoot := filepath.VolumeName(temp) + string(os.PathSeparator)

	for name, root := range map[string]string{
		"empty":           "",
		"relative":        "relative",
		"raw dot dot":     rawParent,
		"filesystem root": volumeRoot,
		"missing":         filepath.Join(temp, "missing"),
		"file":            file,
	} {
		t.Run(name, func(t *testing.T) {
			store, err := OpenFileStore(root)
			if err == nil {
				store.Close()
				t.Fatalf("OpenFileStore(%q) succeeded", root)
			}
		})
	}
}

func TestFileStoreRejectsSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	store, err := OpenFileStore(link)
	if err == nil {
		store.Close()
		t.Fatal("OpenFileStore accepted a final symlink")
	}
}

func TestFileStoreRejectsInvalidPathsAndDirectories(t *testing.T) {
	store, root := newFileStore(t)
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "absolute.html")
	rawParent := "nested" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "report.html"

	for name, path := range map[string]string{
		"empty":              "",
		"dot":                ".",
		"absolute":           abs,
		"dot dot":            ".." + string(os.PathSeparator) + "report.html",
		"raw dot dot":        rawParent,
		"nul":                "report\x00.html",
		"directory":          "directory",
		"trailing separator": "directory" + string(os.PathSeparator),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Write(context.Background(), path, []byte("body")); err == nil {
				t.Fatalf("Write(%q) succeeded", path)
			}
			file, info, err := store.Open(path)
			if err == nil {
				file.Close()
				t.Fatalf("Open(%q) succeeded with %v", path, info)
			}
			if file != nil || info != nil {
				t.Fatalf("Open(%q) returned values on error", path)
			}
		})
	}
}

func TestFileStoreWritesOpensHashesAndSetsModes(t *testing.T) {
	store, root := newFileStore(t)
	body := []byte("hello report")
	result, err := store.Write(context.Background(), filepath.Join("tenant", "2026", "report.html"), body)
	if err != nil {
		t.Fatal(err)
	}
	if result.RelativePath != filepath.Join("tenant", "2026", "report.html") {
		t.Fatalf("RelativePath = %q", result.RelativePath)
	}
	if result.SHA256 != "6dce0a4409fabc637beaa80f9a1d36e0528575f6201c4834d02f6ec7e421fd66" {
		t.Fatalf("SHA256 = %q", result.SHA256)
	}

	file, info, err := store.Open(result.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || !info.Mode().IsRegular() || info.Size() != int64(len(body)) {
		t.Fatalf("opened file = %q, mode = %v, size = %d", got, info.Mode(), info.Size())
	}

	if runtime.GOOS != "windows" {
		for _, dir := range []string{filepath.Join(root, "tenant"), filepath.Join(root, "tenant", "2026")} {
			parentInfo, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := parentInfo.Mode().Perm(); got != 0o750 {
				t.Fatalf("directory %s mode = %04o, want 0750", dir, got)
			}
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Fatalf("file mode = %04o, want 0640", got)
		}
	}
}

func TestFileStoreTreatsSameHashAsRecovery(t *testing.T) {
	store, _ := newFileStore(t)
	body := []byte("same report")
	first, err := store.Write(context.Background(), "report.html", body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Write(context.Background(), "report.html", body)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("recovery result = %#v, want %#v", second, first)
	}
}

func TestFileStoreNeverOverwritesDifferentExistingFile(t *testing.T) {
	store, root := newFileStore(t)
	parent := filepath.Join(root, "nested")
	path := filepath.Join(parent, "report.html")
	injected := false
	var injectErr error
	ctx := newControlledContext(func() bool {
		if injected || !temporaryFileExists(parent) {
			return false
		}
		injectErr = os.WriteFile(path, []byte("existing"), 0o640)
		injected = true
		return false
	})

	if _, err := store.Write(ctx, filepath.Join("nested", "report.html"), []byte("replacement")); err == nil {
		t.Fatal("Write replaced a different existing file")
	}
	if injectErr != nil {
		t.Fatal(injectErr)
	}
	if !injected {
		t.Fatal("test did not create the final file after the temporary file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing file changed to %q", got)
	}
	assertNoTemporaryFiles(t, parent)
}

func TestFileStoreRemovesTemporaryFileAfterCancellation(t *testing.T) {
	store, root := newFileStore(t)
	parent := filepath.Join(root, "nested")
	ctx := newControlledContext(func() bool {
		return temporaryFileExists(parent)
	})
	if _, err := store.Write(ctx, filepath.Join("nested", "report.html"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write error = %v, want context.Canceled", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled Write left files: %v", entries)
	}
}

func TestFileStoreBodyHashHonorsContext(t *testing.T) {
	checks := 0
	ctx := newControlledContext(func() bool {
		checks++
		return checks == 2
	})
	if _, err := sha256HexContext(ctx, make([]byte, 128<<10)); !errors.Is(err, context.Canceled) {
		t.Fatalf("sha256HexContext error = %v, want context.Canceled", err)
	}
}

func TestFileStoreExistingHashHonorsContext(t *testing.T) {
	store, root := newFileStore(t)
	if err := os.WriteFile(filepath.Join(root, "existing.html"), make([]byte, 128<<10), 0o640); err != nil {
		t.Fatal(err)
	}
	checks := 0
	ctx := newControlledContext(func() bool {
		checks++
		return checks == 3
	})
	if _, _, err := store.existingHash(ctx, "existing.html"); !errors.Is(err, context.Canceled) {
		t.Fatalf("existingHash error = %v, want context.Canceled", err)
	}
}

func TestFileStoreExistingHashPrioritizesCancellation(t *testing.T) {
	store, root := newFileStore(t)
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	checks := 0
	ctx := newControlledContext(func() bool {
		checks++
		return checks == 2
	})
	if _, _, err := store.existingHash(ctx, "directory"); !errors.Is(err, context.Canceled) {
		t.Fatalf("existingHash error = %v, want context.Canceled", err)
	}
}

func TestFileStoreCancellationWinsOverCollisionRecovery(t *testing.T) {
	store, root := newFileStore(t)
	parent := filepath.Join(root, "nested")
	path := filepath.Join(parent, "report.html")
	injected := false
	checksAfterTemp := 0
	var injectErr error
	ctx := newControlledContext(func() bool {
		if !injected && temporaryFileExists(parent) {
			injectErr = os.WriteFile(path, nil, 0o640)
			injected = true
			return false
		}
		if injected {
			checksAfterTemp++
			return checksAfterTemp == 3
		}
		return false
	})

	if _, err := store.Write(ctx, filepath.Join("nested", "report.html"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write error = %v, want context.Canceled", err)
	}
	if injectErr != nil {
		t.Fatal(injectErr)
	}
	if !injected {
		t.Fatal("test did not create the final file after the temporary file")
	}
	assertNoTemporaryFiles(t, parent)
}

func TestFileStoreRejectsOutsideSymlinkEscape(t *testing.T) {
	store, root := newFileStore(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.html"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := store.Write(context.Background(), filepath.Join("escape", "report.html"), []byte("body")); err == nil {
		t.Fatal("Write escaped the root through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "report.html")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside report exists or stat failed unexpectedly: %v", err)
	}
	if file, info, err := store.Open(filepath.Join("escape", "secret.html")); err == nil {
		file.Close()
		t.Fatalf("Open escaped the root and returned %v", info)
	}
}

func TestFileStoreOpenRejectsNonRegularTarget(t *testing.T) {
	store, root := newFileStore(t)
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	file, info, err := store.Open("directory")
	if err == nil {
		file.Close()
		t.Fatalf("Open returned non-regular target %v", info)
	}
	if file != nil || info != nil {
		t.Fatal("Open returned file or info for non-regular target")
	}
}

func newFileStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store, root
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}

func temporaryFileExists(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			return true
		}
	}
	return false
}

type controlledContext struct {
	context.Context
	done     chan struct{}
	check    func() bool
	canceled bool
}

func newControlledContext(check func() bool) *controlledContext {
	return &controlledContext{Context: context.Background(), done: make(chan struct{}), check: check}
}

func (ctx *controlledContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *controlledContext) Err() error {
	if !ctx.canceled && ctx.check() {
		ctx.canceled = true
		close(ctx.done)
	}
	if ctx.canceled {
		return context.Canceled
	}
	return nil
}
