package crowdanki

import "fmt"

type Code string

const (
	Syntax     Code = "syntax"
	Schema     Code = "schema"
	Limit      Code = "limit"
	Read       Code = "read"
	Canceled   Code = "canceled"
	Missing    Code = "missing"
	Invalid    Code = "invalid"
	Unresolved Code = "unresolved"
	Ambiguous  Code = "ambiguous"
)

// Error содержит безопасное описание. Unwrap открывает исходную причину
// только для программной диагностики.
type Error struct {
	Code   Code
	Offset int64
	cause  error
}

func (e *Error) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("CrowdAnki: %s (байт %d)", e.Code, e.Offset)
	}
	return "CrowdAnki: " + string(e.Code)
}

func (e *Error) Unwrap() error { return e.cause }

func failure(code Code, offset int64, cause error) *Error {
	return &Error{Code: code, Offset: offset, cause: cause}
}
