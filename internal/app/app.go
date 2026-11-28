// Package app владеет прикладными операциями и контрактом их результатов.
package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"runtime"

	"github.com/FlandreXXX/anki-toolkit/internal/config"
	"github.com/FlandreXXX/anki-toolkit/internal/version"
)

// Operation — стабильное имя операции в машинном ответе.
type Operation string

const (
	VersionOperation Operation = "version"
	DoctorOperation  Operation = "doctor"
	CLIOperation     Operation = "cli"

	// ResultSchema — версия общего машинного ответа.
	ResultSchema = 1
)

// ErrorCode идентифицирует ошибку независимо от её текста.
type ErrorCode string

const (
	UsageError           ErrorCode = "invalid_arguments"
	InvalidConfiguration ErrorCode = "invalid_configuration"
	ConfigurationIO      ErrorCode = "configuration_io"
	Canceled             ErrorCode = "canceled"
)

// Error содержит публикуемые поля. Внутренняя причина доступна через errors.Unwrap.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Source  string    `json:"source,omitzero"`
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Response — версионированное представление прикладного исхода для адаптеров.
type Response struct {
	SchemaVersion int       `json:"schema_version"`
	Operation     Operation `json:"operation"`
	Status        string    `json:"status"`
	Result        any       `json:"result,omitempty"`
	Error         *Error    `json:"error,omitempty"`
}

// VersionResult содержит версию программы.
type VersionResult struct {
	Version string `json:"version"`
}

// PathStatus — результат проверки метаданных пути.
type PathStatus string

const (
	Present     PathStatus = "present"
	Missing     PathStatus = "missing"
	WrongType   PathStatus = "wrong_type"
	Unavailable PathStatus = "unavailable"
)

// PathReport описывает эффективный путь и наблюдаемое состояние.
type PathReport struct {
	config.Path
	Status PathStatus `json:"status"`
}

// DoctorResult описывает исполняемую сборку и пути приложения.
type DoctorResult struct {
	VersionResult
	OS         string     `json:"os"`
	Arch       string     `json:"arch"`
	GoVersion  string     `json:"go_version"`
	ConfigFile PathReport `json:"config_file"`
	DataDir    PathReport `json:"data_dir"`
	CacheDir   PathReport `json:"cache_dir"`
	StateDir   PathReport `json:"state_dir"`
}

// Service выполняет операции напрямую, независимо от способа вызова.
type Service struct {
	Config config.Resolver
}

// Version возвращает каноническую версию без чтения конфигурации.
func (s Service) Version(ctx context.Context) (VersionResult, *Error) {
	if err := ctx.Err(); err != nil {
		return VersionResult{}, cancellation(err)
	}
	return VersionResult{Version: version.Current}, nil
}

// Doctor разрешает конфигурацию и проверяет метаданные путей без записи.
func (s Service) Doctor(ctx context.Context, overrides config.Overrides) (DoctorResult, *Error) {
	build, failure := s.Version(ctx)
	if failure != nil {
		return DoctorResult{}, failure
	}
	settings, err := s.Config.Resolve(ctx, overrides)
	if err := ctx.Err(); err != nil {
		return DoctorResult{}, cancellation(err)
	}
	if err != nil {
		failure := &Error{Code: ConfigurationIO, Message: "Не удалось разрешить конфигурацию.", cause: err}
		var configErr *config.Error
		if errors.As(err, &configErr) {
			failure.Message, failure.Source = configErr.Message, configErr.Source
			if !configErr.IO {
				failure.Code = InvalidConfiguration
			}
		}
		return DoctorResult{}, failure
	}

	result := DoctorResult{VersionResult: build, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version()}
	for _, probe := range []struct {
		dst *PathReport
		src config.Path
		dir bool
	}{
		{&result.ConfigFile, settings.ConfigFile, false},
		{&result.DataDir, settings.DataDir, true},
		{&result.CacheDir, settings.CacheDir, true},
		{&result.StateDir, settings.StateDir, true},
	} {
		if err := ctx.Err(); err != nil {
			return DoctorResult{}, cancellation(err)
		}
		*probe.dst = observe(probe.src, probe.dir)
	}
	if err := ctx.Err(); err != nil {
		return DoctorResult{}, cancellation(err)
	}
	return result, nil
}

func cancellation(err error) *Error {
	return &Error{Code: Canceled, Message: "Операция отменена.", cause: err}
}

func observe(path config.Path, dir bool) PathReport {
	info, err := os.Stat(path.Value)
	var status PathStatus
	switch {
	case errors.Is(err, fs.ErrNotExist):
		status = Missing
	case err != nil:
		status = Unavailable
	case dir && !info.IsDir(), !dir && !info.Mode().IsRegular():
		status = WrongType
	default:
		status = Present
	}
	return PathReport{Path: path, Status: status}
}
