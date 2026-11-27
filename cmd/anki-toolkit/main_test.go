package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/FlandreXXX/anki-toolkit/internal/version"
)

// privateMarker — синтетический маркер приватных данных синтетического окружения.
const privateMarker = "PRIVATE_MARKER"

// privateEvidence — маркеры и признаки сырых системных ошибок, которых не должно
// быть в потоках процесса.
var privateEvidence = []string{
	privateMarker,
	"too many levels",
	"symbolic link",
	"permission denied",
	"Access is denied",
	"sharing violation",
}

// diagnosticKeys — допустимые поля текстовой записи расширенной диагностики.
var diagnosticKeys = map[string]bool{
	"time": true, "level": true, "msg": true,
	"operation": true, "status": true, "code": true, "source": true,
}

// pathExpectation — ожидаемый отчёт о пути.
type pathExpectation struct {
	path   string
	source string
	status string
}

// doctorExpectation — ожидаемые поля успешного результата doctor.
type doctorExpectation struct {
	configFile pathExpectation
	dataDir    pathExpectation
	cacheDir   pathExpectation
	stateDir   pathExpectation
}

// readFailureCase — конфигурация, чтение которой системно отказывает.
type readFailureCase struct {
	name   string
	config string
}

// scenario — проверяемый вызов собранной программы.
type scenario struct {
	name string
	args []string
	exit int
	// machine — ожидается ровно один JSON-документ в stdout.
	machine bool
	// text — ожидаемая подстрока stdout при успехе, stderr при текстовом отказе
	// и публичного сообщения машинного отказа.
	text string
	// operation — ожидаемая операция машинного ответа и записи диагностики.
	operation string
	// code — ожидаемый error.code; пустая строка означает успех.
	code string
	// source — ожидаемый error.source. nil означает, что ключ должен отсутствовать
	// в машинном ответе; в записи диагностики значение тогда не проверяется.
	source  *string
	version bool
	doctor  *doctorExpectation
	verbose bool
}

func TestExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "anki-toolkit")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("Сборка: %v\n%s", err, output)
	}

	home := t.TempDir()
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HOME", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "ANKI_TOOLKIT_CONFIG", "ANKI_TOOLKIT_DATA_DIR":
			return true
		default:
			return false
		}
	})
	env = append(env, "HOME="+home, "APPDATA="+home, "LOCALAPPDATA="+home)

	defaults := platformDefaults(home)
	config := filepath.Join(home, "мои настройки.json")
	dashedConfig := filepath.Join(home, "--ведущие-дефисы.json")
	badConfig := filepath.Join(home, privateMarker+".json")
	fileDataDir := filepath.Join(home, privateMarker+"-file-data")
	dataDir := filepath.Join(home, privateMarker+"-data")
	for path, data := range map[string]string{
		config:       `{"schema_version":1}`,
		dashedConfig: `{"schema_version":1}`,
		badConfig:    `{"schema_version":1,"` + privateMarker + `":true}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Значение data_dir кодируется JSON-кодировщиком: на Windows путь содержит обратные косые.
	fileSettings, err := json.Marshal(map[string]any{"schema_version": 1, "data_dir": fileDataDir})
	if err != nil {
		t.Fatal(err)
	}
	fileConfig := filepath.Join(home, "конфигурация с каталогом данных.json")
	if err := os.WriteFile(fileConfig, fileSettings, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{fileDataDir, dataDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Системные отказы чтения готовятся вне home: цикл ссылок и запрет доступа
	// делают снимок каталога недоступным для чтения.
	blocked := t.TempDir()
	before := snapshot(t, home)

	tests := []scenario{
		{name: "справка без команды", text: "Использование"},
		{name: "глобальная справка", args: []string{"--help"}, text: "doctor"},
		{name: "справка по команде", args: []string{"--json", "doctor", "--help"}, text: "Глобальные параметры перед командой"},
		{name: "версия", args: []string{"version"}, text: version.Current},
		{name: "версия флагом", args: []string{"--version"}, text: version.Current},
		{name: "версия JSON", args: []string{"--json", "version"}, machine: true, operation: "version", version: true},
		{name: "текст диагностики", args: []string{"doctor"}, operation: "doctor", text: "по умолчанию, отсутствует"},
		{name: "текст диагностики с расширенной диагностикой", args: []string{"--verbose", "--config", filepath.Base(fileConfig), "doctor"}, operation: "doctor", verbose: true, text: "существует"},
		{
			name: "диагностика JSON по умолчанию", args: []string{"--json", "doctor"}, machine: true, operation: "doctor",
			doctor: &doctorExpectation{
				configFile: defaults.configFile,
				dataDir:    defaults.dataDir,
				cacheDir:   defaults.cacheDir,
				stateDir:   defaults.stateDir,
			},
		},
		{
			name: "диагностика JSON с путями из файла", args: []string{"--json", "--verbose", "--config", filepath.Base(fileConfig), "doctor"},
			machine: true, operation: "doctor", verbose: true,
			doctor: &doctorExpectation{
				configFile: pathExpectation{fileConfig, "cli", "present"},
				dataDir:    pathExpectation{fileDataDir, "file", "present"},
				cacheDir:   defaults.cacheDir,
				stateDir:   defaults.stateDir,
			},
		},
		{
			name: "диагностика JSON с каталогом данных", args: []string{"--json", "--verbose", "--data-dir", dataDir, "doctor"},
			machine: true, operation: "doctor", verbose: true,
			doctor: &doctorExpectation{
				configFile: defaults.configFile,
				dataDir:    pathExpectation{dataDir, "cli", "present"},
				cacheDir:   defaults.cacheDir,
				stateDir:   defaults.stateDir,
			},
		},
		{
			name: "конфигурация с ведущими дефисами", args: []string{"--json", "--config=" + filepath.Base(dashedConfig), "doctor"},
			machine: true, operation: "doctor",
			doctor: &doctorExpectation{
				configFile: pathExpectation{dashedConfig, "cli", "present"},
				dataDir:    defaults.dataDir,
				cacheDir:   defaults.cacheDir,
				stateDir:   defaults.stateDir,
			},
		},
		{name: "неизвестная команда", args: []string{privateMarker}, exit: 2, text: "Неизвестная команда"},
		{name: "неизвестный параметр", args: []string{"--" + privateMarker, "--json", "doctor"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{name: "расширенная диагностика отказа разбора", args: []string{"--verbose", "--" + privateMarker}, exit: 2, operation: "cli", code: "invalid_arguments", verbose: true, text: "Неизвестный глобальный параметр"},
		{name: "расширенная диагностика отказа разбора в машинном режиме", args: []string{"--json", "--verbose", "--" + privateMarker}, exit: 2, machine: true, operation: "cli", code: "invalid_arguments", verbose: true},
		{name: "машинный отказ неизвестной команды", args: []string{"--json", privateMarker}, exit: 2, machine: true, operation: "cli", code: "invalid_arguments"},
		{name: "повторный --json", args: []string{"--json", "--json", "doctor"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{name: "повторный --config", args: []string{"--json", "--config", "a.json", "--config", "b.json", "doctor"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{name: "взаимоисключающие --help и --version", args: []string{"--help", "--version"}, exit: 2, text: "Передайте только один"},
		{name: "взаимоисключающие --help и --version в машинном режиме", args: []string{"--json", "--help", "--version"}, exit: 2, machine: true, operation: "version", code: "invalid_arguments"},
		{name: "пропущенный путь", args: []string{"--config", "--json", "doctor"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{name: "путь с ведущими дефисами вместо значения", args: []string{"--json", "--config", "--" + privateMarker, "doctor"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{name: "пустой путь", args: []string{"--json", "--data-dir=", "doctor"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{name: "параметр после команды", args: []string{"--json", "doctor", "--verbose"}, exit: 2, machine: true, operation: "doctor", code: "invalid_arguments"},
		{
			name: "относительный каталог данных", args: []string{"--json", "--verbose", "--data-dir=relative", "doctor"},
			exit: 2, machine: true, operation: "doctor", code: "invalid_configuration", source: new("cli"),
			verbose: true, text: "абсолютного пути",
		},
		{
			name: "испорченная конфигурация", args: []string{"--json", "--verbose", "--config", badConfig, "doctor"},
			exit: 2, machine: true, operation: "doctor", code: "invalid_configuration", source: new("cli"),
			verbose: true, text: "Недопустимый JSON",
		},
		{name: "версия с испорченной конфигурацией", args: []string{"--config", badConfig, "version"}, text: version.Current},
		{name: "справка с испорченной конфигурацией", args: []string{"--config", badConfig, "help"}, text: "Использование"},
	}
	// Сценарий configuration_io обязателен по контракту: пустой набор означает,
	// что нативный отказ не проверен, и прогон не должен остаться зелёным.
	failures := readFailureCases(t, blocked)
	if len(failures) == 0 {
		t.Fatal("Набор системных отказов чтения пуст: сценарий configuration_io обязателен.")
	}
	for _, failure := range failures {
		tests = append(tests,
			scenario{
				name: failure.name + ": машинный отказ",
				args: []string{"--json", "--verbose", "--config", failure.config, "doctor"},
				exit: 1, machine: true, verbose: true, operation: "doctor",
				code: "configuration_io", source: new("cli"), text: "Не удалось прочитать файл конфигурации",
			},
			scenario{
				name: failure.name + ": текстовый отказ",
				args: []string{"--verbose", "--config", failure.config, "doctor"},
				exit: 1, verbose: true, operation: "doctor",
				code: "configuration_io", source: new("cli"), text: "Не удалось прочитать файл конфигурации",
			},
		)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(t.Context(), exe, test.args...)
			cmd.Dir, cmd.Stdout, cmd.Stderr = home, &stdout, &stderr
			cmd.Env = env
			err := cmd.Run()
			var exit *exec.ExitError
			if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != test.exit) {
				t.Fatalf("Запуск: %v, stdout %q, stderr %q", err, &stdout, &stderr)
			}
			if err == nil && test.exit != 0 {
				t.Fatalf("Ожидался код %d, stdout %q, stderr %q", test.exit, &stdout, &stderr)
			}
			checkStreams(t, stdout.String(), stderr.String(), test.exit != 0, []string{home, blocked})
			switch {
			case test.machine:
				if !test.verbose && stderr.Len() != 0 {
					t.Fatalf("Машинный режим без --verbose не должен писать в stderr: %q", &stderr)
				}
				checkJSON(t, readDocument(t, &stdout), test)
			case test.exit != 0:
				if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.text) {
					t.Fatalf("Неверный текстовый отказ: stdout %q, stderr %q", &stdout, &stderr)
				}
			default:
				if !strings.Contains(stdout.String(), test.text) || (!test.verbose && stderr.Len() != 0) {
					t.Fatalf("Неверный текстовый вывод: stdout %q, stderr %q", &stdout, &stderr)
				}
				if test.text == version.Current && strings.TrimSpace(stdout.String()) != test.text {
					t.Fatalf("Неверная версия: %q", &stdout)
				}
			}
			if test.verbose {
				checkDiagnostic(t, stderr.String(), test)
			}
		})
	}
	if after := snapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Fatalf("CLI изменил каталог: снимок до %v, после %v", before, after)
	}
	if runtime.GOOS == "linux" {
		t.Run("закрытый канал stdout", func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = read.Close()
			defer write.Close()
			var stderr bytes.Buffer
			cmd := exec.CommandContext(t.Context(), exe, "--json", "version")
			cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = home, env, write, &stderr
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "Не удалось вывести результат") {
				t.Fatalf("Закрытый stdout: ошибка %v, stderr %q", err, &stderr)
			}
		})
	}
}

// platformDefaults вычисляет ожидаемые пути по документированным правилам платформы,
// не обращаясь к продуктовому резолверу. Окружение синтетическое: XDG-переменные
// не заданы, поэтому на Linux и WSL2 действует ветвь HOME.
func platformDefaults(home string) doctorExpectation {
	if runtime.GOOS == "windows" {
		base := filepath.Join(home, "anki-toolkit")
		return doctorExpectation{
			configFile: pathExpectation{filepath.Join(base, "config.json"), "default", "missing"},
			dataDir:    pathExpectation{filepath.Join(base, "data"), "default", "missing"},
			cacheDir:   pathExpectation{filepath.Join(base, "cache"), "default", "missing"},
			stateDir:   pathExpectation{filepath.Join(base, "state"), "default", "missing"},
		}
	}
	return doctorExpectation{
		configFile: pathExpectation{filepath.Join(home, ".config", "anki-toolkit", "config.json"), "default", "missing"},
		dataDir:    pathExpectation{filepath.Join(home, ".local", "share", "anki-toolkit"), "default", "missing"},
		cacheDir:   pathExpectation{filepath.Join(home, ".cache", "anki-toolkit"), "default", "missing"},
		stateDir:   pathExpectation{filepath.Join(home, ".local", "state", "anki-toolkit"), "default", "missing"},
	}
}

// readDocument читает ровно один JSON-документ и возвращает его исходные байты.
func readDocument(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	dec := json.NewDecoder(reader)
	var document json.RawMessage
	if err := dec.Decode(&document); err != nil {
		t.Fatalf("Машинный вывод не читается как JSON: %v", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		t.Fatalf("Ожидался один JSON-документ, получено продолжение: %v", err)
	}
	return document
}

// checkJSON сверяет машинный ответ с ожиданиями сценария. Наличие ключей
// проверяется по карте, значения — по указателям: отсутствие обязательного
// поля ломает тест.
func checkJSON(t *testing.T, document []byte, want scenario) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document, &fields); err != nil {
		t.Fatalf("Машинный ответ не является JSON-объектом: %v", err)
	}
	if schema := requiredField[int](t, fields, "schema_version"); schema != 1 {
		t.Fatalf("schema_version=%d, ожидалось 1", schema)
	}
	if operation := requiredField[string](t, fields, "operation"); operation != want.operation {
		t.Fatalf("operation=%q, ожидалось %q", operation, want.operation)
	}
	status := requiredField[string](t, fields, "status")
	rawResult, hasResult := fields["result"]
	_, hasError := fields["error"]

	if want.code == "" {
		if status != "ok" {
			t.Fatalf("status=%q при успехе, ответ %s", status, document)
		}
		if hasError {
			t.Fatalf("Успешный ответ содержит error: %s", document)
		}
		if !hasResult || string(rawResult) == "null" {
			t.Fatalf("Успешный ответ без result: %s", document)
		}
		switch {
		case want.version:
			checkVersion(t, rawResult)
		case want.doctor != nil:
			checkDoctor(t, rawResult, want.doctor)
		default:
			t.Fatalf("Сценарий не описывает ожидаемый результат: %s", document)
		}
		return
	}

	if status != "error" {
		t.Fatalf("status=%q при отказе, ответ %s", status, document)
	}
	if hasResult {
		t.Fatalf("Отказ содержит result: %s", document)
	}
	if !hasError || string(fields["error"]) == "null" {
		t.Fatalf("Отказ без объекта error: %s", document)
	}
	var failure struct {
		Code    *string `json:"code"`
		Message *string `json:"message"`
		Source  *string `json:"source"`
	}
	if err := json.Unmarshal(fields["error"], &failure); err != nil {
		t.Fatalf("Объект error не разбирается: %v", err)
	}
	if failure.Code == nil || *failure.Code != want.code {
		t.Fatalf("code=%v, ожидалось %q", failure.Code, want.code)
	}
	if failure.Message == nil || *failure.Message == "" || !hasCyrillic(*failure.Message) {
		t.Fatalf("message=%v не является безопасным русским сообщением", failure.Message)
	}
	if want.text != "" && !strings.Contains(*failure.Message, want.text) {
		t.Fatalf("message=%q не содержит %q", *failure.Message, want.text)
	}
	switch {
	case want.source == nil && failure.Source != nil:
		t.Fatalf("source=%q не ожидался", *failure.Source)
	case want.source != nil && failure.Source == nil:
		t.Fatalf("source отсутствует, ожидалось %q", *want.source)
	case want.source != nil && *failure.Source != *want.source:
		t.Fatalf("source=%q, ожидалось %q", *failure.Source, *want.source)
	}
}

// hasCyrillic сообщает, что сообщение отказа на русском языке.
func hasCyrillic(message string) bool {
	return strings.ContainsFunc(message, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) })
}

// requiredField декодирует обязательное поле машинного ответа.
func requiredField[T any](t *testing.T, fields map[string]json.RawMessage, key string) T {
	t.Helper()
	raw, found := fields[key]
	if !found || string(raw) == "null" {
		t.Fatalf("В машинном ответе отсутствует поле %q", key)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("Поле %q не разбирается как JSON: %v", key, err)
	}
	return value
}

// checkVersion сверяет результат операции version.
func checkVersion(t *testing.T, result json.RawMessage) {
	t.Helper()
	var got struct {
		Version *string `json:"version"`
	}
	if err := json.Unmarshal(result, &got); err != nil || got.Version == nil {
		t.Fatalf("Результат версии без поля version: %s", result)
	}
	if *got.Version != version.Current {
		t.Fatalf("version=%q, ожидалось %q", *got.Version, version.Current)
	}
}

// checkDoctor сверяет сведения о сборке и все четыре отчёта о путях.
func checkDoctor(t *testing.T, result json.RawMessage, want *doctorExpectation) {
	t.Helper()
	var got struct {
		Version    *string     `json:"version"`
		OS         *string     `json:"os"`
		Arch       *string     `json:"arch"`
		GoVersion  *string     `json:"go_version"`
		ConfigFile *pathReport `json:"config_file"`
		DataDir    *pathReport `json:"data_dir"`
		CacheDir   *pathReport `json:"cache_dir"`
		StateDir   *pathReport `json:"state_dir"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("Результат диагностики не разбирается: %v", err)
	}
	if got.Version == nil || got.OS == nil || got.Arch == nil || got.GoVersion == nil {
		t.Fatalf("В результате диагностики отсутствуют обязательные поля: %s", result)
	}
	if *got.Version != version.Current || *got.OS != runtime.GOOS || *got.Arch != runtime.GOARCH || *got.GoVersion != runtime.Version() {
		t.Fatalf("Неверные сведения о сборке: %s", result)
	}
	checkPath(t, "config_file", got.ConfigFile, want.configFile)
	checkPath(t, "data_dir", got.DataDir, want.dataDir)
	checkPath(t, "cache_dir", got.CacheDir, want.cacheDir)
	checkPath(t, "state_dir", got.StateDir, want.stateDir)
}

// pathReport — отчёт о пути с обязательными полями.
type pathReport struct {
	Path   *string `json:"path"`
	Source *string `json:"source"`
	Status *string `json:"status"`
}

func checkPath(t *testing.T, name string, got *pathReport, want pathExpectation) {
	t.Helper()
	if got == nil || got.Path == nil || got.Source == nil || got.Status == nil {
		t.Fatalf("%s: отсутствует path, source или status: %+v", name, got)
	}
	if *got.Path != want.path || *got.Source != want.source || *got.Status != want.status {
		t.Fatalf("%s: получено %q, %q, %q; ожидалось %q, %q, %q",
			name, *got.Path, *got.Source, *got.Status, want.path, want.source, want.status)
	}
}

// checkStreams проверяет, что приватные данные и сырые системные ошибки
// не попали в потоки процесса. stdout успешной операции содержит пути по контракту.
func checkStreams(t *testing.T, stdout, stderr string, failed bool, roots []string) {
	t.Helper()
	streams := []struct{ name, content string }{{"stderr", stderr}}
	if failed {
		streams = append(streams, struct{ name, content string }{"stdout", stdout})
	}
	for _, stream := range streams {
		for _, evidence := range privateEvidence {
			if strings.Contains(stream.content, evidence) {
				t.Errorf("Приватные данные %q попали в %s: %q", evidence, stream.name, stream.content)
			}
		}
		for _, root := range roots {
			if strings.Contains(stream.content, root) {
				t.Errorf("Путь %q попал в %s: %q", root, stream.name, stream.content)
			}
		}
	}
}

// attributes разбирает текстовую запись slog по границам ключей и значений.
// ok=false означает, что строка не является набором пар key=value.
func attributes(line string) (map[string]string, bool) {
	attrs := make(map[string]string)
	for rest := line; rest != ""; {
		key, after, found := strings.Cut(rest, "=")
		if !found || key == "" || strings.ContainsAny(key, ` "`) {
			return nil, false
		}
		var value string
		if quoted, err := strconv.QuotedPrefix(after); err == nil {
			unquoted, err := strconv.Unquote(quoted)
			if err != nil {
				return nil, false
			}
			value, rest = unquoted, strings.TrimPrefix(after[len(quoted):], " ")
		} else {
			value, rest, _ = strings.Cut(after, " ")
		}
		attrs[key] = value
	}
	return attrs, true
}

// checkDiagnostic проверяет смысл полей расширенной диагностики, а не подстроку
// всего потока. Timestamp, порядок атрибутов и формат slog не фиксируются.
func checkDiagnostic(t *testing.T, stderr string, want scenario) {
	t.Helper()
	var records []map[string]string
	lines := 0
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		attrs, ok := attributes(line)
		if !ok {
			continue
		}
		if _, found := attrs["operation"]; found {
			records = append(records, attrs)
		}
	}
	if want.machine && lines != 1 {
		t.Fatalf("В машинном режиме stderr содержит %d строк вместо одной записи: %q", lines, stderr)
	}
	if len(records) != 1 {
		t.Fatalf("Ожидалась одна запись диагностики, получено %d: %q", len(records), stderr)
	}
	record := records[0]
	for key := range record {
		if !diagnosticKeys[key] {
			t.Fatalf("Неожиданное поле %q в диагностике: %q", key, stderr)
		}
	}
	if record["operation"] != want.operation {
		t.Fatalf("operation=%q, ожидалось %q", record["operation"], want.operation)
	}
	if want.code == "" {
		if record["status"] != "ok" {
			t.Fatalf("status=%q при успехе: %q", record["status"], stderr)
		}
		for _, key := range []string{"code", "source"} {
			if _, found := record[key]; found {
				t.Fatalf("Поле %q не ожидалось при успехе: %q", key, stderr)
			}
		}
		return
	}
	if record["status"] != "error" {
		t.Fatalf("status=%q при отказе: %q", record["status"], stderr)
	}
	if record["code"] != want.code {
		t.Fatalf("code=%q, ожидалось %q", record["code"], want.code)
	}
	if want.source != nil {
		if got, found := record["source"]; !found || got != *want.source {
			t.Fatalf("source=%q (наличие %t), ожидалось %q", got, found, *want.source)
		}
	}
}

type entrySnapshot struct {
	mode fs.FileMode
	time time.Time
	data string
}

func snapshot(t *testing.T, root string) map[string]entrySnapshot {
	t.Helper()
	entries := make(map[string]entrySnapshot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		entries[path] = entrySnapshot{info.Mode(), info.ModTime(), string(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
