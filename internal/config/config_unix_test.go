//go:build unix

package config

import (
	"context"
	"net"
	"os"
	"os/exec"
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

func TestSocketConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sock")
	socket, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = readConfig(Path{path, "cli"})
	assertConfigError(t, err, "cli", false)
}

func TestFIFOConfig(t *testing.T) {
	const childFIFO = "ANKI_TOOLKIT_TEST_FIFO"
	if fifo := os.Getenv(childFIFO); fifo != "" {
		// Прямой вызов проверяет открытие при подмене пути после проверки типа.
		path := Path{fifo, "cli"}
		file, err := openConfig(fifo)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		_, err = consumeConfig(file, path)
		assertConfigError(t, err, "cli", false)
		_, err = readConfig(path)
		assertConfigError(t, err, "cli", false)
		return
	}

	fifo := filepath.Join(t.TempDir(), "config.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFIFOConfig$")
	cmd.Env = append(cmd.Environ(), childFIFO+"="+fifo)
	// При таймауте CommandContext завершает процесс, а CombinedOutput ждёт его выхода.
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("Проверка FIFO превысила таймаут, дочерний процесс завершён: %v\n%s", err, output)
	}
	if err != nil {
		t.Fatalf("Проверка FIFO в дочернем процессе: %v\n%s", err, output)
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
