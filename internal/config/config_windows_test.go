//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigWrongTypeWindows проверяет нативный сценарий Windows: каталог,
// выбранный вместо файла, отклоняется по типу открытого объекта.
func TestConfigWrongTypeWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "каталог конфигурации")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := readConfig(Path{dir, "cli"})
	assertConfigError(t, err, "cli", false)

	file, openErr := openConfig(dir)
	if openErr != nil {
		// Windows может отказать в открытии каталога на чтение; тогда предел
		// доказательства — отказ на уровне open, а не проверки дескриптора.
		return
	}
	defer file.Close()
	if _, err := consumeConfig(file, Path{dir, "cli"}); err == nil {
		t.Fatal("Каталог принят как обычный файл")
	} else {
		assertConfigError(t, err, "cli", false)
	}
}

// TestConfigRegularFileWindows фиксирует чтение обычного файла с пробелами и
// Unicode в пути.
func TestConfigRegularFileWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "настройки с пробелом.json")
	want := filepath.Join(t.TempDir(), "данные")
	writeConfig(t, path, want)
	got, err := readConfig(Path{path, "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Прочитано %q, ожидалось %q", got, want)
	}
}
