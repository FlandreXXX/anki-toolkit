//go:build linux || windows

package crowdankifs_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ca "github.com/FlandreXXX/anki-toolkit/internal/crowdanki"
	cafs "github.com/FlandreXXX/anki-toolkit/internal/crowdankifs"
)

const export = "  {\"__type__\":\"Deck\",\"media_files\":[\"../нет\"]}\n"

func requireFileCode(t *testing.T, err error, want cafs.Code) {
	t.Helper()
	var got *cafs.Error
	if !errors.As(err, &got) || got.Code != want {
		t.Fatalf("ошибка %v, ожидался код %s", err, want)
	}
}

func TestReadExport(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	first := filepath.Join(parent, "Колода с пробелом")
	second := filepath.Join(parent, "第二")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(first, "deck.json")
	if err := os.WriteFile(path, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	// Сопутствующие данные могут отсутствовать или быть непрозрачными.
	if err := os.WriteFile(filepath.Join(first, "import_config.yaml"), []byte("[битые данные"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(dir string) {
		t.Helper()
		before, err := os.Stat(filepath.Join(dir, "deck.json"))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := cafs.Read(ctx, dir, ca.Limits{MaxBytes: int64(len(export))})
		if err != nil || !bytes.Equal(doc.Original(), []byte(export)) {
			t.Fatalf("чтение: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "deck.json"))
		if err != nil || string(data) != export {
			t.Fatalf("повреждён исходный файл: %v", err)
		}
		after, err := os.Stat(filepath.Join(dir, "deck.json"))
		if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("файл заменён или изменён: %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 2 {
			t.Fatalf("изменён состав каталога: %v", err)
		}
		_, err = cafs.Read(ctx, dir, ca.Limits{MaxBytes: int64(len(export) - 1)})
		var limit *ca.Error
		if !errors.As(err, &limit) || limit.Code != ca.Limit {
			t.Fatalf("ожидалось превышение размера: %v", err)
		}
	}
	check(first)
	if err := os.Rename(first, second); err != nil {
		t.Fatal(err)
	}
	check(second)
}

func TestInvalidExportSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, err := cafs.Read(ctx, "", ca.Limits{})
	requireFileCode(t, err, cafs.DirectoryRequired)
	_, err = cafs.Read(ctx, ".", ca.Limits{})
	requireFileCode(t, err, cafs.DirectoryRequired)
	_, err = cafs.Read(ctx, filepath.Join(dir, "приватный путь"), ca.Limits{})
	requireFileCode(t, err, cafs.Missing)
	if strings.Contains(err.Error(), "приватный") || !errors.Is(err, os.ErrNotExist) {
		t.Fatal("небезопасное описание или потеря причины")
	}
	_, err = cafs.Read(ctx, dir, ca.Limits{})
	requireFileCode(t, err, cafs.Missing)
	wrongDir := filepath.Join(dir, "обычный файл")
	if err := os.WriteFile(wrongDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = cafs.Read(ctx, wrongDir, ca.Limits{})
	requireFileCode(t, err, cafs.WrongType)
	if err := os.Mkdir(filepath.Join(dir, "deck.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = cafs.Read(ctx, dir, ca.Limits{})
	requireFileCode(t, err, cafs.WrongType)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = cafs.Read(canceled, dir, ca.Limits{})
	requireFileCode(t, err, cafs.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("потеряна причина отмены")
	}
}

func TestRejectDeckSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	for _, owner := range []string{dir, outside} {
		if err := os.WriteFile(filepath.Join(owner, "target.json"), []byte(export), 0o600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(owner, "target.json")
		link := filepath.Join(dir, "deck.json")
		if err := os.Symlink(target, link); err != nil {
			skipSymlink(t, err)
		}
		_, err := cafs.Read(context.Background(), dir, ca.Limits{})
		requireFileCode(t, err, cafs.WrongType)
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
}
