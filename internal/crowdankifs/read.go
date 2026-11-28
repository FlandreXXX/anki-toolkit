// Package crowdankifs читает deck.json одного явно указанного экспорта.
package crowdankifs

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/FlandreXXX/anki-toolkit/internal/crowdanki"
)

const deckJSON = "deck.json"

type Code string

const (
	DirectoryRequired Code = "directory_required"
	Missing           Code = "missing"
	WrongType         Code = "wrong_type"
	Unavailable       Code = "unavailable"
	Canceled          Code = "canceled"
)

type Error struct {
	Code  Code
	cause error
}

func (e *Error) Error() string { return "Чтение экспорта CrowdAnki: " + string(e.Code) }
func (e *Error) Unwrap() error { return e.cause }

var (
	errWrongType   = errors.New("ожидался обычный файл deck.json в каталоге экспорта")
	errUnavailable = errors.New("файловый источник недоступен")
)

// Read принимает только абсолютный путь, открывает один обычный файл для
// чтения и передаёт его чистому парсеру.
func Read(ctx context.Context, dir string, limits crowdanki.Limits) (*crowdanki.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, &Error{Code: Canceled, cause: err}
	}
	if !filepath.IsAbs(dir) {
		return nil, &Error{Code: DirectoryRequired}
	}
	file, err := openRegular(filepath.Clean(dir))
	if err != nil {
		code := Unavailable
		switch {
		case ctx.Err() != nil:
			return nil, &Error{Code: Canceled, cause: ctx.Err()}
		case errors.Is(err, errWrongType):
			code = WrongType
		case !errors.Is(err, errUnavailable) && errors.Is(err, fs.ErrNotExist):
			code = Missing
		}
		return nil, &Error{Code: code, cause: err}
	}
	defer file.Close()
	return crowdanki.Parse(ctx, file, limits)
}
