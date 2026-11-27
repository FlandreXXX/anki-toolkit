//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestConsumeConfigReadsOpenedObject доказывает, что решение принимается по
// дескриптору: имя заменяется после открытия, а читается исходный объект.
func TestConsumeConfigReadsOpenedObject(t *testing.T) {
	base := t.TempDir()
	want := filepath.Join(base, "данные")
	for _, test := range []struct {
		name    string
		replace func(t *testing.T, path string)
	}{
		{"FIFO", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"каталог", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(base, "подмена "+test.name+".json")
			writeConfig(t, path, want)
			file, err := openConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			test.replace(t, path)

			got, err := consumeConfig(file, Path{path, "cli"})
			if err != nil {
				t.Fatalf("Чтение открытого объекта отклонено: %v", err)
			}
			if got != want {
				t.Fatalf("Прочитано %q, ожидалось %q", got, want)
			}
		})
	}
}

// TestSpecialFileConfig проверяет отказ на специальном объекте, подменившем
// обычный файл. Вызов ограничен по времени: ожидание на FIFO недопустимо.
func TestSpecialFileConfig(t *testing.T) {
	base := t.TempDir()
	fifo := filepath.Join(base, "конфигурация.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		data string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		data, err := readConfig(Path{fifo, "cli"})
		done <- outcome{data, err}
	}()
	select {
	case got := <-done:
		if got.data != "" || got.err == nil {
			t.Fatalf("Специальный объект принят: %q, %v", got.data, got.err)
		}
		assertConfigError(t, got.err, "cli", false)
	case <-time.After(10 * time.Second):
		t.Fatal("Открытие FIFO заблокировалось")
	}
}

// TestConfigSymlinks фиксирует обработку ссылок: допустимая ссылка на обычный
// файл читается, оборванная ссылка — системный отказ, а не пустая настройка.
func TestConfigSymlinks(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "цель с пробелом.json")
	want := filepath.Join(base, "данные")
	writeConfig(t, target, want)
	link := filepath.Join(base, "ссылка.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Символические ссылки недоступны: %v", err)
	}
	got, err := readConfig(Path{link, "cli"})
	if err != nil || got != want {
		t.Fatalf("Допустимая ссылка не прочитана: %q, %v", got, err)
	}

	dangling := filepath.Join(base, "оборванная.json")
	if err := os.Symlink(filepath.Join(base, "нет.json"), dangling); err != nil {
		t.Fatal(err)
	}
	_, err = readConfig(Path{dangling, "default"})
	assertConfigError(t, err, "default", true)
	_, err = readConfig(Path{dangling, "cli"})
	assertConfigError(t, err, "cli", false)
}
