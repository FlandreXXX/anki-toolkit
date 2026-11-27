package app_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/FlandreXXX/anki-toolkit/internal/app"
	"github.com/FlandreXXX/anki-toolkit/internal/config"
	"github.com/FlandreXXX/anki-toolkit/internal/version"
)

func TestOperations(t *testing.T) {
	base := t.TempDir()
	env := map[string]string{"HOME": base, "APPDATA": base, "LOCALAPPDATA": base}
	service := app.Service{Config: config.Resolver{LookupEnv: func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}}}
	t.Run("версия без конфигурации", func(t *testing.T) {
		got, failure := (app.Service{}).Version(t.Context())
		if failure != nil || got.Version != version.Current {
			t.Fatalf("Версия %+v, ошибка %v", got, failure)
		}
	})
	t.Run("диагностика со значениями по умолчанию", func(t *testing.T) {
		got, failure := service.Doctor(t.Context(), config.Overrides{})
		if failure != nil {
			t.Fatal(failure)
		}
		if got.Version != version.Current || got.OS != runtime.GOOS || got.Arch != runtime.GOARCH || got.GoVersion != runtime.Version() {
			t.Fatalf("Неверные сведения о сборке: %+v", got)
		}
		for _, path := range []app.PathReport{got.ConfigFile, got.DataDir, got.CacheDir, got.StateDir} {
			if path.Status != app.Missing || path.Source != "default" || !filepath.IsAbs(path.Value) {
				t.Fatalf("Неверный статус отсутствующего пути: %+v", path)
			}
		}
	})
	t.Run("существующие файл и каталог", func(t *testing.T) {
		path := filepath.Join(base, "config.json")
		if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		got, failure := service.Doctor(t.Context(), config.Overrides{ConfigFile: &path, DataDir: &base})
		if failure != nil || got.ConfigFile.Status != app.Present || got.DataDir.Status != app.Present {
			t.Fatalf("Диагностика %+v, ошибка %v", got, failure)
		}
		got, failure = service.Doctor(t.Context(), config.Overrides{DataDir: &path})
		if failure != nil || got.DataDir.Status != app.WrongType {
			t.Fatalf("Диагностика %+v, ошибка %v", got, failure)
		}
	})
	t.Run("отказ сохраняет причину", func(t *testing.T) {
		path := filepath.Join(base, "missing.json")
		_, failure := service.Doctor(t.Context(), config.Overrides{ConfigFile: &path})
		if failure == nil || failure.Code != app.InvalidConfiguration || failure.Source != "cli" || !errors.Is(failure, os.ErrNotExist) {
			t.Fatalf("Неверный результат отказа: %+v", failure)
		}
	})
	t.Run("недоступный путь", func(t *testing.T) {
		// Цикл символических ссылок даёт ошибку ELOOP: путь существует, но
		// метаданные получить нельзя. Ни fs.ErrNotExist, ни неверный тип.
		loop, err := symlinkLoop(base, "цикл")
		if err != nil {
			t.Skipf("Создание символических ссылок недоступно: %v", err)
		}
		path := filepath.Join(base, "config.json")
		if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		got, failure := service.Doctor(t.Context(), config.Overrides{ConfigFile: &path, DataDir: &loop})
		if failure != nil {
			t.Fatal(failure)
		}
		if got.ConfigFile.Status != app.Present {
			t.Fatalf("Верный файл конфигурации не подтверждён: %+v", got.ConfigFile)
		}
		if got.DataDir.Status != app.Unavailable {
			t.Fatalf("Ожидался статус unavailable, получено %+v", got.DataDir)
		}
		for _, unaffected := range []app.PathReport{got.CacheDir, got.StateDir} {
			if unaffected.Status != app.Missing {
				t.Fatalf("Затронут посторонний путь: %+v", unaffected)
			}
		}
		// Нечитаемый файл конфигурации — отказ операции, а не наблюдение статуса
		// и не ложный missing.
		if _, failure := service.Doctor(t.Context(), config.Overrides{ConfigFile: &loop}); failure == nil ||
			failure.Code != app.ConfigurationIO || errors.Is(failure, fs.ErrNotExist) {
			t.Fatalf("Неверный результат отказа конфигурации: %+v", failure)
		}
	})
	t.Run("отмена", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, versionFailure := (app.Service{}).Version(ctx)
		_, doctorFailure := (app.Service{}).Doctor(ctx, config.Overrides{})
		for _, failure := range []*app.Error{versionFailure, doctorFailure} {
			if failure == nil || failure.Code != app.Canceled || !errors.Is(failure, context.Canceled) {
				t.Fatalf("Неверный результат отмены: %v", failure)
			}
		}
	})
	t.Run("отмена во время разрешения конфигурации", func(t *testing.T) {
		// Отмена приходит после начала разрешения конфигурации: первое чтение
		// окружения отменяет контекст и возвращает отсутствие значения.
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		service := app.Service{Config: config.Resolver{LookupEnv: func(key string) (string, bool) {
			if calls == 0 {
				calls++
				cancel()
				return "", false
			}
			value, ok := env[key]
			return value, ok
		}}}
		got, failure := service.Doctor(ctx, config.Overrides{})
		if calls == 0 {
			t.Fatal("Окружение не читалось: разрешение конфигурации не начиналось")
		}
		if failure == nil || failure.Code != app.Canceled || !errors.Is(failure, context.Canceled) {
			t.Fatalf("Неверный результат отмены: %v", failure)
		}
		if got != (app.DoctorResult{}) {
			t.Fatalf("При отмене получен результат: %+v", got)
		}
	})
}

// symlinkLoop создаёт пару взаимных символических ссылок и возвращает путь
// первой из них. Ошибка создания возвращается вызывающему коду: на части
// платформ символические ссылки требуют прав, и тогда сценарий пропускается.
func symlinkLoop(dir, name string) (string, error) {
	first, second := filepath.Join(dir, name+"-1"), filepath.Join(dir, name+"-2")
	if err := os.Symlink(second, first); err != nil {
		return "", err
	}
	if err := os.Symlink(first, second); err != nil {
		return "", err
	}
	return first, nil
}
