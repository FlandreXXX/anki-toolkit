package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/FlandreXXX/anki-toolkit/internal/app"
	"github.com/FlandreXXX/anki-toolkit/internal/cli"
)

type unavailableWriter struct{}

func (unavailableWriter) Write([]byte) (int, error) {
	return 0, errors.New("PRIVATE_MARKER")
}

func TestOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"version"}, {"--json", "version"}} {
		var stderr bytes.Buffer
		exit := cli.Run(t.Context(), app.Service{}, args, unavailableWriter{}, &stderr)
		if exit != 1 || !strings.Contains(stderr.String(), "Не удалось вывести результат") || strings.Contains(stderr.String(), "PRIVATE_MARKER") {
			t.Fatalf("Код %d и stderr %q после отказа записи", exit, &stderr)
		}
	}
	if exit := cli.Run(t.Context(), app.Service{}, []string{"--verbose", "version"}, io.Discard, unavailableWriter{}); exit != 1 {
		t.Fatalf("Код %d после отказа записи диагностики", exit)
	}
}

func TestCanceledExit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout bytes.Buffer
	exit := cli.Run(ctx, app.Service{}, []string{"--json", "doctor"}, &stdout, io.Discard)
	if exit != 130 || !strings.Contains(stdout.String(), `"code":"canceled"`) {
		t.Fatalf("Код %d и stdout %q после отмены", exit, &stdout)
	}
}
