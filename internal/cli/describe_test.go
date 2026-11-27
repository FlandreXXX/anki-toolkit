package cli

import (
	"strings"
	"testing"

	"github.com/FlandreXXX/anki-toolkit/internal/app"
	"github.com/FlandreXXX/anki-toolkit/internal/config"
)

// TestDescribe проверяет русские подписи источников и статусов текстового
// контракта. Ключи карты обязаны совпадать с каноническими константами config.
func TestDescribe(t *testing.T) {
	for _, test := range []struct {
		source string
		status app.PathStatus
		want   string
	}{
		{config.SourceDefault, app.Present, "по умолчанию, существует"},
		{config.SourceFile, app.Missing, "файл, отсутствует"},
		{config.SourceEnvironment, app.WrongType, "окружение, объект другого типа"},
		{config.SourceCLI, app.Unavailable, "CLI, проверка недоступна"},
	} {
		report := app.PathReport{Status: test.status}
		report.Value, report.Source = "/tmp/путь", test.source
		got := describe("Конфигурация", report)
		if !strings.HasPrefix(got, `Конфигурация: "/tmp/путь" `) || !strings.HasSuffix(got, "("+test.want+")") {
			t.Fatalf("describe(%q, %q) = %q, ожидался суффикс %q", test.source, test.status, got, test.want)
		}
	}
}
