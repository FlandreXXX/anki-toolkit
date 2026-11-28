// Package config разрешает пути и читает пользовательскую конфигурацию.
package config

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	configSchema = 1
	maxFileSize  = 64 << 10
)

// Канонические идентификаторы источников значения в машинном протоколе.
const (
	SourceDefault     = "default"
	SourceFile        = "file"
	SourceEnvironment = "environment"
	SourceCLI         = "cli"
)

// Overrides различает отсутствующие флаги и явно переданные пустые значения.
type Overrides struct {
	ConfigFile *string
	DataDir    *string
}

// Path связывает эффективный путь с его источником.
type Path struct {
	Value  string `json:"path"`
	Source string `json:"source"`
}

// Settings содержит пути, разрешённые без создания файлов и каталогов.
type Settings struct {
	ConfigFile Path
	DataDir    Path
	CacheDir   Path
	StateDir   Path
}

// Error хранит безопасное описание и внутреннюю причину отказа.
type Error struct {
	Source  string
	Message string
	IO      bool
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Resolver читает окружение через переданную функцию, а файлы через ОС.
type Resolver struct {
	LookupEnv func(string) (string, bool)
}

// Resolve строго читает выбранный файл и применяет приоритеты настроек.
func (r Resolver) Resolve(ctx context.Context, overrides Overrides) (Settings, error) {
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	configPath, err := r.configPath(overrides.ConfigFile)
	if err != nil {
		return Settings{}, err
	}
	dataFromFile, err := readConfig(configPath)
	if err != nil {
		return Settings{}, err
	}
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}

	data, found := r.override(overrides.DataDir, "ANKI_TOOLKIT_DATA_DIR")
	switch {
	case found:
		if !absolute(data.Value) {
			return Settings{}, &Error{Source: data.Source, Message: "Каталог данных требует допустимого абсолютного пути."}
		}
		data.Value = filepath.Clean(data.Value)
	case dataFromFile != "":
		data = Path{Value: filepath.Clean(dataFromFile), Source: SourceFile}
	default:
		data, err = r.defaultPath("data")
		if err != nil {
			return Settings{}, err
		}
	}
	cache, err := r.defaultPath("cache")
	if err != nil {
		return Settings{}, err
	}
	state, err := r.defaultPath("state")
	if err != nil {
		return Settings{}, err
	}
	return Settings{ConfigFile: configPath, DataDir: data, CacheDir: cache, StateDir: state}, nil
}

func (r Resolver) override(value *string, env string) (Path, bool) {
	if value != nil {
		return Path{Value: *value, Source: SourceCLI}, true
	}
	if v, ok := r.LookupEnv(env); ok {
		return Path{Value: v, Source: SourceEnvironment}, true
	}
	return Path{}, false
}

func (r Resolver) configPath(value *string) (Path, error) {
	p, ok := r.override(value, "ANKI_TOOLKIT_CONFIG")
	if !ok {
		p, err := r.defaultPath("config")
		if err != nil {
			return Path{}, err
		}
		p.Value = filepath.Join(p.Value, "config.json")
		return p, nil
	}
	if absolute(p.Value) {
		p.Value = filepath.Clean(p.Value)
		return p, nil
	}
	if !validPath(p.Value) || filepath.VolumeName(p.Value) != "" ||
		os.IsPathSeparator(p.Value[0]) || !validRelative(p.Value) {
		return Path{}, &Error{Source: p.Source, Message: "Недопустимый путь к файлу конфигурации."}
	}
	full, err := filepath.Abs(p.Value)
	if err != nil {
		return Path{}, &Error{Source: p.Source, Message: "Не удалось разрешить относительный путь к файлу конфигурации.", IO: true, cause: err}
	}
	if !absolute(full) {
		return Path{}, &Error{Source: p.Source, Message: "Недопустимый путь к файлу конфигурации."}
	}
	p.Value = full
	return p, nil
}

func validPath(path string) bool {
	return strings.TrimSpace(path) != "" && utf8.ValidString(path) && strings.IndexFunc(path, unicode.IsControl) < 0
}

func absolute(path string) bool {
	return validPath(path) && filepath.IsAbs(path) && validAbsolute(path)
}

func readConfig(p Path) (string, error) {
	// Некоторые специальные объекты нельзя открыть. Ошибки Stat не заменяют
	// результат открытия, а тип успешно открытого объекта проверяется повторно.
	if info, err := os.Stat(p.Value); err == nil && !info.Mode().IsRegular() {
		return "", configTypeError(p)
	}
	file, err := openConfig(p.Value)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist) && p.Source == SourceDefault:
			// Отсутствие стандартного файла допустимо. Отсутствие по Lstat
			// отличает его от оборванной символической ссылки.
			if _, linkErr := os.Lstat(p.Value); errors.Is(linkErr, fs.ErrNotExist) {
				return "", nil
			} else if linkErr != nil {
				return "", configIO(p, linkErr)
			}
			return "", configIO(p, err)
		case errors.Is(err, fs.ErrNotExist):
			return "", &Error{Source: p.Source, Message: "Указанный файл конфигурации отсутствует.", cause: err}
		default:
			return "", configIO(p, err)
		}
	}
	defer file.Close()
	return consumeConfig(file, p)
}

// consumeConfig проверяет и читает уже открытый объект по его дескриптору.
// Тип определяется по метаданным открытого объекта, поэтому замена имени между
// открытием и проверкой не может подменить читаемые данные.
//
// ponytail: содержимое файла после открытия не защищено от чужой записи; снимок
// на уровне дескриптора потребовал бы изменения контракта.
func consumeConfig(file *os.File, p Path) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", configIO(p, err)
	}
	if !info.Mode().IsRegular() {
		return "", configTypeError(p)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return "", configIO(p, err)
	}
	if len(raw) > maxFileSize {
		return "", &Error{Source: p.Source, Message: "Размер файла конфигурации превышает 64 КиБ."}
	}
	return decodeConfig(raw, p.Source)
}

func configTypeError(p Path) *Error {
	return &Error{Source: p.Source, Message: "Конфигурация должна быть обычным файлом."}
}

func configIO(p Path, err error) *Error {
	return &Error{Source: p.Source, Message: "Не удалось прочитать файл конфигурации.", IO: true, cause: err}
}

func decodeConfig(raw []byte, source string) (string, error) {
	var input struct {
		SchemaVersion int            `json:"schema_version"`
		DataDir       jsontext.Value `json:"data_dir"`
	}
	if err := json.Unmarshal(raw, &input, json.RejectUnknownMembers(true)); err != nil {
		return "", &Error{Source: source, Message: "Недопустимый JSON или поля файла конфигурации.", cause: err}
	}
	if input.SchemaVersion != configSchema {
		return "", &Error{Source: source, Message: "Требуется schema_version со значением 1."}
	}
	if len(input.DataDir) == 0 {
		return "", nil
	}
	var path string
	if err := json.Unmarshal(input.DataDir, &path); err != nil || !absolute(path) {
		return "", &Error{Source: source, Message: "Поле data_dir требует допустимого абсолютного пути.", cause: err}
	}
	return path, nil
}
