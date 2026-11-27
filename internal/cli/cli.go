// Package cli преобразует аргументы и результаты на границе процесса.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/FlandreXXX/anki-toolkit/internal/app"
	"github.com/FlandreXXX/anki-toolkit/internal/config"
)

const helpText = `Anki Toolkit

Использование: anki-toolkit [глобальные параметры] [команда]

Команды:
  help             Показать справку.
  version          Показать версию программы.
  doctor           Показать сведения о сборке, конфигурации и каталогах.

Глобальные параметры перед командой:
  --help           Показать справку.
  --version        Показать версию программы.
  --json           Вернуть один JSON-результат. Справка остаётся текстовой.
  --verbose        Записать расширенную диагностику в stderr.
  --config ПУТЬ    Выбрать JSON-файл (относительные пути считаются от рабочего каталога).
  --data-dir ПУТЬ  Переопределить каталог данных (абсолютный путь).

После version и doctor допустим только --help. Пути с пробелами заключайте в кавычки.
Для путей, начинающихся с --, используйте --config=ПУТЬ или --data-dir=ПУТЬ.
Примеры:
  anki-toolkit --json version
  anki-toolkit --json --verbose --config "./мои настройки.json" doctor
`

type invocation struct {
	operation app.Operation
	overrides config.Overrides
	machine   bool
	verbose   bool
	help      bool
	failure   string
}

func parse(args []string) invocation {
	in := invocation{operation: app.CLIOperation}
	seen := make(map[string]bool)
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		arg := args[0]
		args = args[1:]
		name, value, assigned := strings.Cut(arg, "=")
		if seen[name] {
			in.reject("Глобальный параметр передан повторно.")
		}
		seen[name] = true
		switch name {
		case "--config", "--data-dir":
			if !assigned {
				if len(args) == 0 || strings.HasPrefix(args[0], "--") {
					in.reject("Отсутствует путь после глобального параметра.")
					continue
				}
				value, args = args[0], args[1:]
			}
			if strings.TrimSpace(value) == "" {
				in.reject("Явно заданный путь не может быть пустым.")
			}
			if name == "--config" {
				in.overrides.ConfigFile = &value
			} else {
				in.overrides.DataDir = &value
			}
		case "--help", "--version", "--json", "--verbose":
			if assigned {
				in.reject("Глобальный параметр не принимает значения.")
				continue
			}
			switch name {
			case "--help":
				in.help = true
			case "--version":
				in.operation = app.VersionOperation
			case "--json":
				in.machine = true
			case "--verbose":
				in.verbose = true
			}
		default:
			in.reject("Неизвестный глобальный параметр. См. --help.")
		}
	}
	if len(args) == 0 {
		in.help = in.help || in.operation == app.CLIOperation
		if in.help && in.operation == app.VersionOperation {
			in.reject("Передайте только один из параметров --help и --version.")
		}
		return in
	}
	if in.help || in.operation == app.VersionOperation {
		in.reject("Параметры --help и --version используются без команды.")
	}
	switch args[0] {
	case "help":
		in.help = true
		if len(args) != 1 {
			in.reject("Команда help не принимает аргументы.")
		}
	case "version", "doctor":
		in.operation = app.Operation(args[0])
		if len(args) == 2 && args[1] == "--help" {
			in.help = true
		} else if len(args) != 1 {
			in.reject("После команды допустим только --help.")
		}
	default:
		in.reject("Неизвестная команда. См. --help.")
	}
	return in
}

func (in *invocation) reject(message string) {
	if in.failure == "" {
		in.failure = message
	}
}

// Run вызывает прикладную операцию и возвращает код завершения процесса.
func Run(ctx context.Context, service app.Service, args []string, stdout, stderr io.Writer) int {
	in := parse(args)
	out := app.Response{SchemaVersion: app.ResultSchema, Operation: in.operation, Status: "ok"}
	if in.failure != "" {
		out.Error = &app.Error{Code: app.UsageError, Message: in.failure}
	} else if in.help {
		_, err := io.WriteString(stdout, helpText)
		return finish(stderr, err, 0)
	} else {
		switch in.operation {
		case app.VersionOperation:
			out.Result, out.Error = service.Version(ctx)
		case app.DoctorOperation:
			out.Result, out.Error = service.Doctor(ctx, in.overrides)
		}
	}

	exit := 0
	if out.Error != nil {
		out.Status, out.Result = "error", nil
		switch out.Error.Code {
		case app.UsageError, app.InvalidConfiguration:
			exit = 2
		case app.Canceled:
			exit = 130
		default:
			exit = 1
		}
	}
	renderErr := render(in.machine, out, stdout, stderr)
	if in.verbose {
		record := slog.NewRecord(time.Now(), slog.LevelInfo, "Операция завершена", 0)
		record.Add("operation", out.Operation, "status", out.Status)
		if out.Error != nil {
			record.Add("code", out.Error.Code, "source", out.Error.Source)
		}
		renderErr = errors.Join(renderErr, slog.NewTextHandler(stderr, nil).Handle(ctx, record))
	}
	return finish(stderr, renderErr, exit)
}

func render(machine bool, out app.Response, stdout, stderr io.Writer) error {
	if machine {
		return json.NewEncoder(stdout).Encode(out)
	}
	if out.Error != nil {
		_, err := fmt.Fprintln(stderr, out.Error.Message)
		return err
	}
	switch result := out.Result.(type) {
	case app.VersionResult:
		_, err := fmt.Fprintln(stdout, result.Version)
		return err
	case app.DoctorResult:
		_, err := fmt.Fprintf(stdout, "Anki Toolkit %s\nПлатформа: %s/%s\nGo: %s\n%s\n%s\n%s\n%s\n",
			result.Version, result.OS, result.Arch, result.GoVersion,
			describe("Конфигурация", result.ConfigFile), describe("Данные", result.DataDir),
			describe("Кеш", result.CacheDir), describe("Состояние", result.StateDir))
		return err
	default:
		return fmt.Errorf("Отсутствует результат операции.")
	}
}

func describe(label string, report app.PathReport) string {
	sources := map[string]string{"default": "по умолчанию", "file": "файл", "environment": "окружение", "cli": "CLI"}
	statuses := map[app.PathStatus]string{
		app.Present: "существует", app.Missing: "отсутствует",
		app.WrongType: "объект другого типа", app.Unavailable: "проверка недоступна",
	}
	return fmt.Sprintf("%s: %q (%s, %s)", label, report.Value, sources[report.Source], statuses[report.Status])
}

func finish(stderr io.Writer, err error, exit int) int {
	if err != nil {
		fmt.Fprintln(stderr, "Не удалось вывести результат.")
		return 1
	}
	return exit
}
