package app_test

import (
	"context"
	"errors"
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
}
