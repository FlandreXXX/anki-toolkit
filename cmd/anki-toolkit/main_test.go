package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FlandreXXX/anki-toolkit/internal/version"
)

func TestExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "anki-toolkit")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("Сборка: %v\n%s", err, output)
	}

	home := t.TempDir()
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "ANKI_TOOLKIT_CONFIG", "ANKI_TOOLKIT_DATA_DIR":
			return true
		default:
			return false
		}
	})
	env = append(env, "HOME="+home, "APPDATA="+home, "LOCALAPPDATA="+home)
	config := filepath.Join(home, "мои настройки.json")
	badConfig := filepath.Join(home, "PRIVATE_MARKER.json")
	for path, data := range map[string]string{config: `{"schema_version":1}`, badConfig: `{"PRIVATE_MARKER":12}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, home)

	for _, test := range []struct {
		name       string
		args       []string
		exit       int
		operation  string
		errorCode  string
		text       string
		diagnostic bool
	}{
		{name: "справка без команды", text: "Использование"},
		{name: "глобальная справка", args: []string{"--help"}, text: "doctor"},
		{name: "справка по команде", args: []string{"--json", "doctor", "--help"}, text: "Глобальные параметры перед командой"},
		{name: "версия", args: []string{"version"}, text: version.Current},
		{name: "версия флагом", args: []string{"--version"}, text: version.Current},
		{name: "версия JSON", args: []string{"--json", "version"}, operation: "version"},
		{name: "текст диагностики", args: []string{"doctor"}, text: "отсутствует"},
		{name: "диагностика JSON", args: []string{"--json", "--verbose", "--config", filepath.Base(config), "doctor"}, operation: "doctor", diagnostic: true},
		{name: "неизвестная команда", args: []string{"PRIVATE_MARKER"}, exit: 2, text: "Неизвестная команда"},
		{name: "неизвестный параметр", args: []string{"--PRIVATE_MARKER", "--json", "doctor"}, exit: 2, operation: "doctor", errorCode: "invalid_arguments"},
		{name: "машинный отказ неизвестной команды", args: []string{"--json", "PRIVATE_MARKER"}, exit: 2, operation: "cli", errorCode: "invalid_arguments"},
		{name: "пропущенный путь", args: []string{"--config", "--json", "doctor"}, exit: 2, operation: "doctor", errorCode: "invalid_arguments"},
		{name: "пустой путь", args: []string{"--json", "--data-dir=", "doctor"}, exit: 2, operation: "doctor", errorCode: "invalid_arguments"},
		{name: "параметр после команды", args: []string{"--json", "doctor", "--verbose"}, exit: 2, operation: "doctor", errorCode: "invalid_arguments"},
		{name: "испорченная конфигурация", args: []string{"--json", "--verbose", "--config", badConfig, "doctor"}, exit: 2, operation: "doctor", errorCode: "invalid_configuration", diagnostic: true},
		{name: "версия с испорченной конфигурацией", args: []string{"--config", badConfig, "version"}, text: version.Current},
		{name: "справка с испорченной конфигурацией", args: []string{"--config", badConfig, "help"}, text: "Использование"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(t.Context(), exe, test.args...)
			cmd.Dir, cmd.Stdout, cmd.Stderr = home, &stdout, &stderr
			cmd.Env = env
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != test.exit {
					t.Fatalf("Запуск: %v, stdout %q, stderr %q", err, &stdout, &stderr)
				}
			} else if test.exit != 0 {
				t.Fatalf("Ожидался код %d, stdout %q, stderr %q", test.exit, &stdout, &stderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), "PRIVATE_MARKER") {
				t.Fatal("Приватный маркер попал в вывод")
			}
			if test.operation == "" {
				if test.exit != 0 {
					if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.text) {
						t.Fatalf("Неверный текстовый отказ: stdout %q, stderr %q", &stdout, &stderr)
					}
					return
				}
				if !strings.Contains(stdout.String(), test.text) || stderr.Len() != 0 {
					t.Fatalf("Неверный текстовый вывод: stdout %q, stderr %q", &stdout, &stderr)
				}
				if test.text == version.Current && strings.TrimSpace(stdout.String()) != test.text {
					t.Fatalf("Неверная версия: %q", &stdout)
				}
				return
			}
			if test.diagnostic && !strings.Contains(stderr.String(), "operation=doctor") || !test.diagnostic && stderr.Len() != 0 {
				t.Fatalf("Неверный stderr: %q", &stderr)
			}
			checkJSON(t, &stdout, test.operation, test.errorCode, config)
		})
	}
	if after := snapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Fatalf("CLI изменил каталог: снимок до %v, после %v", before, after)
	}
	if runtime.GOOS == "linux" {
		t.Run("закрытый канал stdout", func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = read.Close()
			defer write.Close()
			var stderr bytes.Buffer
			cmd := exec.CommandContext(t.Context(), exe, "--json", "version")
			cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = home, env, write, &stderr
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "Не удалось вывести результат") {
				t.Fatalf("Закрытый stdout: ошибка %v, stderr %q", err, &stderr)
			}
		})
	}
}

func checkJSON(t *testing.T, stdout io.Reader, operation, errorCode, configPath string) {
	t.Helper()
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Operation     string          `json:"operation"`
		Status        string          `json:"status"`
		Result        json.RawMessage `json:"result"`
		Error         *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	dec := json.NewDecoder(stdout)
	if err := dec.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		t.Fatalf("Ожидался один JSON-документ, получено %v", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Operation != operation {
		t.Fatalf("Неверный конверт: %+v", envelope)
	}
	if errorCode != "" {
		if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != errorCode || envelope.Error.Message == "" || envelope.Result != nil {
			t.Fatalf("Неверный JSON-отказ: %+v", envelope)
		}
		return
	}
	if envelope.Status != "ok" || envelope.Error != nil {
		t.Fatalf("Неверный JSON-успех: %+v", envelope)
	}
	var result struct {
		Version   string `json:"version"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		GoVersion string `json:"go_version"`
		DataDir   struct {
			Status string `json:"status"`
		} `json:"data_dir"`
		ConfigFile struct {
			Path   string `json:"path"`
			Source string `json:"source"`
			Status string `json:"status"`
		} `json:"config_file"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != version.Current {
		t.Fatalf("Неверная версия: %+v", result)
	}
	if operation == "doctor" && (result.OS != runtime.GOOS || result.Arch != runtime.GOARCH ||
		result.GoVersion != runtime.Version() || result.ConfigFile.Path != configPath ||
		result.ConfigFile.Source != "cli" || result.ConfigFile.Status != "present" || result.DataDir.Status != "missing") {
		t.Fatalf("Неверный результат диагностики: %+v", result)
	}
}

type entrySnapshot struct {
	mode fs.FileMode
	time time.Time
	data string
}

func snapshot(t *testing.T, root string) map[string]entrySnapshot {
	t.Helper()
	entries := make(map[string]entrySnapshot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		entries[path] = entrySnapshot{info.Mode(), info.ModTime(), string(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
