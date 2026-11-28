package crowdankifs_test

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	ca "github.com/FlandreXXX/anki-toolkit/internal/crowdanki"
	cafs "github.com/FlandreXXX/anki-toolkit/internal/crowdankifs"
)

func skipSymlink(t *testing.T, err error) {
	t.Helper()
	t.Fatal(err)
}

func TestRejectSpecialFiles(t *testing.T) {
	const childDir = "CROWDANKI_TEST_SPECIAL_DIR"
	if dir := os.Getenv(childDir); dir != "" {
		_, err := cafs.Read(context.Background(), dir, ca.Limits{})
		requireFileCode(t, err, cafs.WrongType)
		_, err = cafs.Read(context.Background(), filepath.Join(dir, "deck.json"), ca.Limits{})
		requireFileCode(t, err, cafs.WrongType)
		return
	}
	for _, kind := range []string{"FIFO", "сокет"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "deck.json")
			if kind == "FIFO" {
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRejectSpecialFiles$")
			cmd.Env = append(os.Environ(), childDir+"="+dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("дочерняя проверка %v, контекст %v\n%s", err, ctx.Err(), out)
			}
		})
	}
}

func TestReadDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("процесс root может читать файл без разрешений")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deck.json"), []byte(export), 0); err != nil {
		t.Fatal(err)
	}
	_, err := cafs.Read(context.Background(), dir, ca.Limits{})
	requireFileCode(t, err, cafs.Unavailable)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal("потеряна причина отказа в доступе")
	}
}
