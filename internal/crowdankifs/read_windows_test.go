package crowdankifs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	ca "github.com/FlandreXXX/anki-toolkit/internal/crowdanki"
	cafs "github.com/FlandreXXX/anki-toolkit/internal/crowdankifs"
)

func skipSymlink(t *testing.T, err error) {
	t.Helper()
	const privilegeNotHeld syscall.Errno = 1314
	if errors.Is(err, privilegeNotHeld) {
		t.Skip("Windows не разрешает создавать символические ссылки этому процессу")
	}
	t.Fatal(err)
}

func TestReadDenied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.json")
	if err := os.WriteFile(path, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	_, err = cafs.Read(context.Background(), dir, ca.Limits{})
	requireFileCode(t, err, cafs.Unavailable)
	const sharingViolation syscall.Errno = 32
	if !errors.Is(err, sharingViolation) {
		t.Fatal("потеряна причина отказа в открытии")
	}
}
