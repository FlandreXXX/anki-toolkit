package config

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"HOME": home, "APPDATA": home, "LOCALAPPDATA": home,
		"XDG_CONFIG_HOME": home, "XDG_DATA_HOME": home,
	}
	resolver := resolverFor(env)
	cfg := filepath.Join(home, "anki-toolkit", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0700); err != nil {
		t.Fatal(err)
	}
	defaultData := filepath.Dir(cfg)
	if runtime.GOOS == "windows" {
		defaultData = filepath.Join(defaultData, "data")
	}
	check := func(overrides Overrides, wantFile, wantData Path) {
		t.Helper()
		got, err := resolver.Resolve(t.Context(), overrides)
		if err != nil {
			t.Fatal(err)
		}
		if got.ConfigFile != wantFile || got.DataDir != wantData {
			t.Fatalf("Получены пути %+v, ожидались %+v и %+v", got, wantFile, wantData)
		}
	}
	defaultFile := Path{cfg, "default"}
	check(Overrides{}, defaultFile, Path{defaultData, "default"})
	if _, err := os.Stat(cfg); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Чтение создало стандартную конфигурацию или завершилось ошибкой: %v", err)
	}

	fileData := filepath.Join(home, "данные из файла")
	writeConfig(t, cfg, fileData)
	check(Overrides{}, defaultFile, Path{fileData, "file"})

	envFile, envFileData := filepath.Join(home, "настройки окружения.json"), filepath.Join(home, "данные второго файла")
	writeConfig(t, envFile, envFileData)
	env["ANKI_TOOLKIT_CONFIG"] = envFile
	check(Overrides{}, Path{envFile, "environment"}, Path{envFileData, "file"})
	envData := filepath.Join(home, "из окружения")
	env["ANKI_TOOLKIT_DATA_DIR"] = envData
	check(Overrides{}, Path{envFile, "environment"}, Path{envData, "environment"})

	t.Chdir(home)
	relative := filepath.Join("..", filepath.Base(home), "настройки CLI.json")
	cliFile := filepath.Join(home, "настройки CLI.json")
	cliData := filepath.Join(home, "данные CLI")
	writeConfig(t, cliFile, fileData)
	env["ANKI_TOOLKIT_CONFIG"], env["ANKI_TOOLKIT_DATA_DIR"] = "", ""
	check(Overrides{&relative, &cliData}, Path{cliFile, "cli"}, Path{cliData, "cli"})

	_, err := resolver.Resolve(t.Context(), Overrides{})
	assertConfigError(t, err, "environment", false)
	_, err = resolver.Resolve(t.Context(), Overrides{ConfigFile: &relative})
	assertConfigError(t, err, "environment", false)
}

func TestFileValidation(t *testing.T) {
	base := t.TempDir()
	validPath, err := json.Marshal(filepath.Join(base, "мои данные"))
	if err != nil {
		t.Fatal(err)
	}
	// Независимые от реализации литералы: контракт 64 КиБ = 65 536 байт.
	const (
		schemaOnly = `{"schema_version":1}`
		limit      = 65536
	)
	pad := func(n int) string { return schemaOnly + strings.Repeat(" ", n-len(schemaOnly)) }
	for _, test := range []struct {
		name, raw string
		valid     bool
	}{
		{"только схема", schemaOnly, true},
		{"Unicode и пробелы", `{"schema_version":1,"data_dir":` + string(validPath) + `}`, true},
		{"граница минус один", pad(limit - 1), true},
		{"точная граница", pad(limit), true},
		{"граница плюс один", pad(limit + 1), false},
		{"синтаксис", `{"schema_version":`, false},
		{"неизвестный ключ", `{"schema_version":1,"PRIVATE_MARKER":true}`, false},
		{"регистр ключа", `{"SCHEMA_VERSION":1}`, false},
		{"повтор ключа после декодирования", `{"schema_version":1,"schema_versio\u006e":1}`, false},
		{"повтор data_dir", `{"schema_version":1,"data_dir":null,"data_dir":null}`, false},
		{"версия отсутствует", `{}`, false},
		{"версия не поддерживается", `{"schema_version":2}`, false},
		{"строковая версия", `{"schema_version":"1"}`, false},
		{"дробная версия", `{"schema_version":1.0}`, false},
		{"версия null", `{"schema_version":null}`, false},
		{"второй документ", `{"schema_version":1} {}`, false},
		{"хвост", `{"schema_version":1} PRIVATE_MARKER`, false},
		{"верхний массив", `[{"schema_version":1}]`, false},
		{"верхний null", `null`, false},
		{"неверный UTF-8", "{\"schema_version\":1,\"\xff\":true}", false},
		{"пустой путь", `{"schema_version":1,"data_dir":""}`, false},
		{"пробельный путь", `{"schema_version":1,"data_dir":"  "}`, false},
		{"относительный путь", `{"schema_version":1,"data_dir":"PRIVATE_MARKER"}`, false},
		{"числовой путь", `{"schema_version":1,"data_dir":12}`, false},
		{"путь null", `{"schema_version":1,"data_dir":null}`, false},
		{"управляющий символ", `{"schema_version":1,"data_dir":"/PRIVATE_MARKER\u0000"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(base, "настройки.json")
			if err := os.WriteFile(path, []byte(test.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readConfig(Path{path, "cli"})
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertConfigError(t, err, "cli", false)
		})
	}
}

func TestInvalidInputsAndCancellation(t *testing.T) {
	base := t.TempDir()
	r := resolverFor(map[string]string{"HOME": base, "APPDATA": base, "LOCALAPPDATA": base})
	missing := filepath.Join(base, "PRIVATE_MARKER.json")
	for _, path := range []string{"", " ", "\x00", missing, base} {
		_, err := r.Resolve(t.Context(), Overrides{ConfigFile: &path})
		assertConfigError(t, err, "cli", false)
	}
	for _, path := range []string{"", " ", "relative", "\x00"} {
		_, err := r.Resolve(t.Context(), Overrides{DataDir: &path})
		assertConfigError(t, err, "cli", false)
	}
	if runtime.GOOS == "windows" {
		for _, path := range []string{`C:PRIVATE_MARKER`, `\PRIVATE_MARKER`, `\\?\C:\PRIVATE_MARKER`, `\\.\NUL`, `C:\NUL.txt`, `C:\a:stream`, `NUL/../PRIVATE_MARKER`} {
			_, err := r.Resolve(t.Context(), Overrides{ConfigFile: &path})
			assertConfigError(t, err, "cli", false)
			_, err = r.Resolve(t.Context(), Overrides{DataDir: &path})
			assertConfigError(t, err, "cli", false)
		}
		if !absolute(`\\server\share\каталог с пробелом`) {
			t.Fatal("Отклонён абсолютный путь UNC")
		}
	}
	_, err := (resolverFor(nil)).Resolve(t.Context(), Overrides{})
	assertConfigError(t, err, "default", false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Resolve(ctx, Overrides{}); err != context.Canceled {
		t.Fatalf("Ожидалась отмена, получено %v", err)
	}
}

func TestPlatformDefaults(t *testing.T) {
	base := t.TempDir()
	env := map[string]string{"HOME": base, "APPDATA": filepath.Join(base, "roaming"), "LOCALAPPDATA": filepath.Join(base, "local")}
	r := resolverFor(env)
	check := func(config, data, cache, state string) {
		t.Helper()
		got, err := r.Resolve(t.Context(), Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		want := Settings{
			ConfigFile: Path{filepath.Join(base, config, "config.json"), "default"},
			DataDir:    Path{filepath.Join(base, data), "default"},
			CacheDir:   Path{filepath.Join(base, cache), "default"},
			StateDir:   Path{filepath.Join(base, state), "default"},
		}
		if got != want {
			t.Fatalf("Пути %+v, ожидались %+v", got, want)
		}
	}
	if runtime.GOOS == "windows" {
		check("roaming/anki-toolkit", "local/anki-toolkit/data", "local/anki-toolkit/cache", "local/anki-toolkit/state")
		env["LOCALAPPDATA"] = "relative"
		_, err := r.Resolve(t.Context(), Overrides{})
		assertConfigError(t, err, "default", false)
		return
	}
	env["XDG_CONFIG_HOME"], env["XDG_DATA_HOME"], env["XDG_CACHE_HOME"], env["XDG_STATE_HOME"] = "relative", "relative", "relative", "relative"
	check(".config/anki-toolkit", ".local/share/anki-toolkit", ".cache/anki-toolkit", ".local/state/anki-toolkit")
	for _, kind := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		env["XDG_"+kind+"_HOME"] = filepath.Join(base, kind)
	}
	delete(env, "HOME")
	check("CONFIG/anki-toolkit", "DATA/anki-toolkit", "CACHE/anki-toolkit", "STATE/anki-toolkit")
}

func TestUnreadableFile(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("Проверка режима 000 требует непривилегированного Linux")
	}
	base := t.TempDir()
	path := filepath.Join(base, "PRIVATE_MARKER.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0000); err != nil {
		t.Fatal(err)
	}
	_, err := readConfig(Path{path, "cli"})
	assertConfigError(t, err, "cli", true)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Не сохранена причина отказа: %v", err)
	}
}

func resolverFor(env map[string]string) Resolver {
	return Resolver{LookupEnv: func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}}
}

func writeConfig(t *testing.T, path, data string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "data_dir": data})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertConfigError(t *testing.T, err error, source string, ioFailure bool) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Source != source || got.IO != ioFailure {
		t.Fatalf("Ожидался отказ источника %s (IO=%t), получено %v", source, ioFailure, err)
	}
	if strings.Contains(err.Error(), "PRIVATE_MARKER") {
		t.Fatal("Ошибка содержит приватный маркер")
	}
}
