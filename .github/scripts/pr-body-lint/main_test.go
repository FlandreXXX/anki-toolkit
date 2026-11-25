package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// sectionText — содержательный текст раздела для синтетических входов.
var sectionText = map[string]string{
	"Цель":        "Сделать проверку описания PR предсказуемой.",
	"Изменения":   "Добавлен валидатор описания и тонкий CI-адаптер.",
	"Проверки":    "go test и go vet выполнены локально.",
	"CI":          "Запуск ещё не завершён, отметка pending.",
	"Ограничения": "Существенных ограничений нет.",
}

func bodyWith(overrides map[string]string) string {
	texts := maps.Clone(sectionText)
	maps.Copy(texts, overrides)
	parts := make([]string, 0, len(requiredSections))
	for _, name := range requiredSections {
		parts = append(parts, "## "+name+"\n"+texts[name])
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func validBody() string { return bodyWith(nil) }

func TestValidate(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantOK     bool
		wantErrors []string
	}{
		{name: "все разделы с текстом", body: validBody(), wantOK: true},
		{
			name:   "дополнительный раздел между обязательными",
			body:   strings.Replace(validBody(), "## Проверки", "## Безопасность\nСекреты не добавлены.\n\n## Проверки", 1),
			wantOK: true,
		},
		{
			name:       "отсутствующий раздел",
			body:       withoutSection("CI"),
			wantErrors: []string{"Отсутствует обязательный заголовок: ## CI"},
		},
		{
			name:       "повторяющийся раздел",
			body:       validBody() + "\n## Цель\nПовторный раздел.\n",
			wantErrors: []string{"Заголовок повторяется: ## Цель"},
		},
		{name: "переставленные разделы", body: swappedBody(), wantErrors: []string{"порядок"}},
		{
			name:       "раздел только в комментарии",
			body:       "<!--\n" + validBody() + "\n-->\n",
			wantErrors: []string{"Отсутствует обязательный заголовок: ## Цель"},
		},
		{
			name:       "незакрытый комментарий скрывает остаток",
			body:       "## Цель\nИнформация.\n<!--\n## Изменения\nИнформация.\n## Проверки\nИнформация.\n",
			wantErrors: []string{"## Изменения"},
		},
		{
			name:       "заголовки в fenced-блоке",
			body:       "```md\n" + validBody() + "```\n",
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "тильда-ограждение",
			body:       "~~~md\n" + validBody() + "~~~\n",
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "info string внутри блока не закрывает его",
			body:       "```text\n```md\n" + validBody() + "```\n",
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "более длинный маркер не закрывается коротким",
			body:       "````md\n```\n" + validBody() + "````\n",
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "разные маркеры не закрывают друг друга",
			body:       "~~~md\n" + validBody() + "```\n",
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "незакрытый fenced-блок скрывает остаток",
			body:       "```md\n" + validBody(),
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "заголовок в блок-цитате",
			body:       "> ## Цель\n> Информация.\n" + strings.Replace(validBody(), "## Цель\n", "", 1),
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "заголовок с отступом кода",
			body:       "    ## Цель\n    Информация.\n" + strings.Replace(validBody(), "## Цель\n", "", 1),
			wantErrors: []string{"## Цель"},
		},
		{
			name:       "заголовок с отступом табуляции",
			body:       "\t## Цель\n\tИнформация.\n" + strings.Replace(validBody(), "## Цель\n", "", 1),
			wantErrors: []string{"## Цель"},
		},
		{
			name:   "корректные закрывающие решётки не входят в имя",
			body:   strings.Replace(bodyWith(nil), "## Цель", "## Цель ###", 1),
			wantOK: true,
		},
		{
			name:   "заголовки первого и третьего уровня не считаются разделами",
			body:   "# Цель\nИнформация.\n\n### Изменения\nИнформация.\n\n" + validBody(),
			wantOK: true,
		},
		{
			name:       "пустой раздел",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "   ", 1),
			wantErrors: []string{"Пустой обязательный раздел: ## CI"},
		},
		{
			name:       "раздел только из горизонтальной линии",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "---", 1),
			wantErrors: []string{"Пустой обязательный раздел: ## CI"},
		},
		{
			name:       "раздел только из код-блока",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "```\nэто пример\n```", 1),
			wantErrors: []string{"Пустой обязательный раздел: ## CI"},
		},
		{
			name:       "заглушка TODO",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "TODO", 1),
			wantErrors: []string{"заглушку: ## CI"},
		},
		{
			name:       "заглушка с многоточием",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "…", 1),
			wantErrors: []string{"заглушку: ## CI"},
		},
		{
			name:       "заглушка из шаблонной подсказки",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "<статус CI>", 1),
			wantErrors: []string{"заглушку: ## CI"},
		},
		{
			name:       "несколько заглушек вместо текста",
			body:       strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "TODO\nTBD\n—", 1),
			wantErrors: []string{"заглушку: ## CI"},
		},
		{
			name:   "содержательная фраза об отсутствии ограничений принимается",
			body:   bodyWith(map[string]string{"Ограничения": "Существенных ограничений нет."}),
			wantOK: true,
		},
		{
			name:   "честный статус невыполненной проверки принимается",
			body:   bodyWith(map[string]string{"CI": "not run: workflow ещё не добавлен в base."}),
			wantOK: true,
		},
		{
			name:   "комментарий рядом с текстом не скрывает текст",
			body:   strings.Replace(validBody(), "Запуск ещё не завершён, отметка pending.", "<!-- пояснение -->\nФакт CI.", 1),
			wantOK: true,
		},
		{
			name:   "маркер комментария внутри fenced-блока литерален",
			body:   validBody() + "\n```md\n<!-- скрытый пример -->\n```\n",
			wantOK: true,
		},
		{
			name:       "пустое описание",
			body:       "\n \n",
			wantErrors: []string{"пустое"},
		},
		{
			name:       "null-строка не является содержанием",
			body:       "null\n",
			wantErrors: []string{"Отсутствует обязательный заголовок: ## Цель"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validate(c.body)
			if c.wantOK {
				if len(got) != 0 {
					t.Fatalf("ожидался успех, получены ошибки: %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("ожидалась ошибка, нарушений не найдено")
			}
			for _, want := range c.wantErrors {
				found := false
				for _, problem := range got {
					if strings.Contains(problem, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("в диагностике нет %q: %v", want, got)
				}
			}
		})
	}
}

// swappedBody меняет местами разделы «Цель» и «Изменения».
func swappedBody() string {
	swapped := strings.Replace(validBody(), "## Цель", "## ВРЕМЕННО", 1)
	swapped = strings.Replace(swapped, "## Изменения", "## Цель", 1)
	return strings.Replace(swapped, "## ВРЕМЕННО", "## Изменения", 1)
}

// withoutSection убирает обязательный раздел вместе с его текстом.
func withoutSection(name string) string {
	return strings.Replace(validBody(), "## "+name+"\n"+sectionText[name], "", 1)
}

func TestValidateReorder(t *testing.T) {
	got := validate(swappedBody())
	if len(got) == 0 {
		t.Fatal("ожидалась ошибка порядка разделов")
	}
	if !strings.Contains(strings.Join(got, "\n"), "порядок") {
		t.Fatalf("диагностика не сообщает о порядке: %v", got)
	}
}

// TestValidatePlaceholderRejected доказывает, что чистая функция отклоняет
// заглушку и пустой раздел, а не только CLI.
func TestValidatePlaceholderRejected(t *testing.T) {
	for name, text := range map[string]string{
		"todo":      "TODO",
		"tbd":       "TBD",
		"заполнить": "заполнить",
		"уточнить":  "уточнить",
		"точки":     "...",
		"тире":      "—",
		"дефис":     "-",
		"нет":       "нет",
		"n/a":       "n/a",
		"вопрос":    "?",
	} {
		t.Run(name, func(t *testing.T) {
			body := bodyWith(map[string]string{"Ограничения": text})
			if got := validate(body); len(got) == 0 {
				t.Fatalf("заглушка %q принята", text)
			}
		})
	}
}

func TestCodePointLimit(t *testing.T) {
	base := validBody()
	if count := utf8.RuneCountInString(base); count >= maxBodyPoints {
		t.Fatalf("базовый body уже длиннее лимита: %d", count)
	}
	exact := base + strings.Repeat("😀", maxBodyPoints-utf8.RuneCountInString(base))
	if got := validate(exact); len(got) != 0 {
		t.Fatalf("ровно %d code points отклонены: %v", maxBodyPoints, got)
	}
	if got := validate(exact + "😀"); len(got) == 0 {
		t.Fatalf("превышение лимита на один code point принято")
	} else if !strings.Contains(strings.Join(got, "\n"), "10001") {
		t.Fatalf("диагностика лимита без фактического числа: %v", got)
	}
	// Кириллица: байты превышают лимит, code points — нет.
	cyrillic := base + strings.Repeat("я", maxBodyPoints-utf8.RuneCountInString(base))
	if len(cyrillic) <= maxBodyPoints {
		t.Fatalf("ожидался вход длиннее лимита в байтах: %d", len(cyrillic))
	}
	if got := validate(cyrillic); len(got) != 0 {
		t.Fatalf("кириллица у границы отклонена: %v", got)
	}
}

func TestLineEndingsNormalized(t *testing.T) {
	base := validBody()
	for name, body := range map[string]string{
		"crlf": strings.ReplaceAll(base, "\n", "\r\n"),
		"cr":   strings.ReplaceAll(base, "\n", "\r"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := validate(body); len(got) != 0 {
				t.Fatalf("окончания строк не нормализованы: %v", got)
			}
		})
	}
	// CRLF не должен съедать один code point из лимита.
	base = validBody()
	exact := base + strings.Repeat("😀", maxBodyPoints-utf8.RuneCountInString(base))
	crlf := strings.ReplaceAll(exact, "\n", "\r\n")
	if got := validate(crlf); len(got) != 0 {
		t.Fatalf("CRLF сдвинул границу лимита: %v", got)
	}
	if got := validate(crlf + "😀"); len(got) == 0 {
		t.Fatalf("граница после нормализации CRLF не проверяется")
	}
}

func TestShellMetacharactersAreData(t *testing.T) {
	text := "Оболочка не вызывается: | $(id) `whoami` ; rm -rf / не выполняются."
	body := bodyWith(map[string]string{"Проверки": text + "\n\n```sh\n<!-- не комментарий, а литерал -->\n$ echo $(id)\n```"})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("разметка в тексте обработана как код: %v", got)
	}
	// Полный недоверенный body не попадает в вывод.
	problems := validate(strings.Replace(body, "## Цель", "## Цель $(id) |", 1))
	for _, problem := range problems {
		if strings.Contains(problem, "$(id)") || strings.Contains(problem, "whoami") {
			t.Fatalf("диагностика воспроизводит body: %v", problem)
		}
	}
}

func TestNestedListFence(t *testing.T) {
	nested := "- ```markdown\n" + strings.Join(prefixLines(strings.Split(validBody(), "\n"), "  "), "\n") + "\n  ```\n"
	if got := validate(nested); len(got) == 0 {
		t.Fatal("пример внутри пункта списка засчитан как разделы body")
	}
	for _, indent := range []string{"  ", "    ", "\t"} {
		t.Run("indent="+indent, func(t *testing.T) {
			fenced := "- ```md\n" + strings.Join(prefixLines(strings.Split(validBody(), "\n"), indent+"  "), "\n") + "\n" + indent + "  ```\n"
			if got := validate(fenced); len(got) == 0 {
				t.Fatal("вложенный fenced-блок засчитан как разделы body")
			}
		})
	}
	// Содержательный текст пункта списка остаётся содержанием раздела.
	body := bodyWith(map[string]string{"Изменения": "- валидатор проверяет пять разделов\n- второй пункт списка"})
	if got := validate(body); len(got) != 0 {
		t.Fatalf("обычный список отклонён: %v", got)
	}
}

func prefixLines(lines []string, prefix string) []string {
	result := make([]string, len(lines))
	for i, line := range lines {
		result[i] = prefix + line
	}
	return result
}

// writeTemp создаёт файл во временном каталоге теста и возвращает путь.
func writeTemp(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("не удалось создать %s: %v", name, err)
	}
	return path
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUsageErrors(t *testing.T) {
	body := writeTemp(t, "body.md", []byte(validBody()))
	event := writeTemp(t, "event.json", []byte(`{"pull_request":{"body":"## Цель\nТекст."}}`))
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "без режима", args: nil, wantErr: "ровно один режим"},
		{name: "оба режима", args: []string{"--body-file", body, "--event-file", event}, wantErr: "взаимоисключающие"},
		{name: "неизвестный флаг", args: []string{"--bogus", "x"}, wantErr: "Ошибка использования"},
		{name: "лишний позиционный аргумент", args: []string{"--body-file", body, "лишнее"}, wantErr: "лишние позиционные"},
		{name: "флаг без значения", args: []string{"--body-file"}, wantErr: "Ошибка использования"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(c.args...)
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if !strings.Contains(stderr, c.wantErr) {
				t.Fatalf("в stderr нет %q: %q", c.wantErr, stderr)
			}
			if stdout != "" {
				t.Fatalf("неожиданный вывод в stdout: %q", stdout)
			}
		})
	}
}

func TestInputErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого-файла.md")
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "отсутствующий body-файл", args: []string{"--body-file", missing}, wantErr: "Ошибка входа"},
		{name: "нечитаемый event-файл", args: []string{"--event-file", missing}, wantErr: "Ошибка входа"},
		{name: "некорректный UTF-8 в body", args: []string{"--body-file", writeTemp(t, "bad.md", []byte{0xff, 0xfe, 0x20})}, wantErr: "UTF-8"},
		{name: "некорректный UTF-8 в событии", args: []string{"--event-file", writeTemp(t, "bad.json", []byte{0xff, 0x00})}, wantErr: "UTF-8"},
		{name: "некорректный JSON", args: []string{"--event-file", writeTemp(t, "broken.json", []byte(`{"pull_request":`))}, wantErr: "JSON"},
		{name: "нет объекта pull_request", args: []string{"--event-file", writeTemp(t, "no-pr.json", []byte(`{"action":"edited"}`))}, wantErr: "pull_request"},
		{name: "pull_request null", args: []string{"--event-file", writeTemp(t, "null-pr.json", []byte(`{"pull_request":null}`))}, wantErr: "pull_request"},
		{name: "pull_request не объект", args: []string{"--event-file", writeTemp(t, "array-pr.json", []byte(`{"pull_request":[]}`))}, wantErr: "не является объектом"},
		{name: "body не строка", args: []string{"--event-file", writeTemp(t, "num-body.json", []byte(`{"pull_request":{"body":42}}`))}, wantErr: "строкой"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr := runCLI(c.args...)
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
			}
			if !strings.Contains(stderr, c.wantErr) {
				t.Fatalf("в stderr нет %q: %q", c.wantErr, stderr)
			}
		})
	}
}

func TestManualEmptyBodyIsViolation(t *testing.T) {
	for name, payload := range map[string]string{
		"null":     `{"pull_request":{"body":null,"user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
		"пустой":   `{"pull_request":{"body":"","user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
		"пробелы":  `{"pull_request":{"body":"  \n\t\n","user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
		"нет поля": `{"pull_request":{"user":{"login":"someone","type":"User"},"head":{"ref":"feat/x"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, "event.json", []byte(payload))
			code, stdout, stderr := runCLI("--event-file", path)
			if code != 1 {
				t.Fatalf("код выхода %d, ожидался 1", code)
			}
			if stderr == "" {
				t.Fatal("нарушение контракта без диагностики")
			}
			if stdout != "" {
				t.Fatalf("неожиданный вывод в stdout: %q", stdout)
			}
		})
	}
}

// eventJSON собирает синтетический снимок события pull_request.
func eventJSON(t *testing.T, body, login, accountType, branch string) string {
	t.Helper()
	pr := map[string]any{
		"body":  body,
		"title": "renovate[bot] renovate/update",
		"user":  map[string]any{"login": login, "type": accountType},
		"head":  map[string]any{"ref": branch},
		"labels": []map[string]any{
			{"name": "dependencies"},
		},
	}
	if login == "" && accountType == "" {
		pr["user"] = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{
		"action":       "synchronize",
		"sender":       map[string]any{"login": "renovate[bot]", "type": "Bot"},
		"pull_request": pr,
	})
	if err != nil {
		t.Fatalf("не удалось собрать событие: %v", err)
	}
	return string(payload)
}

func TestBotExemption(t *testing.T) {
	// Исключение проверяется на заведомо непригодном body: успех возможен
	// только за счёт сработавшего исключения.
	bad := "## Цель\nTODO\n"
	cases := []struct {
		name        string
		login       string
		accountType string
		branch      string
		wantCode    int
	}{
		{name: "renovate бот с префиксом", login: "renovate[bot]", accountType: "Bot", branch: "renovate/github-actions", wantCode: 0},
		{name: "dependabot бот с префиксом", login: "dependabot[bot]", accountType: "Bot", branch: "dependabot/go_modules/x", wantCode: 0},
		{name: "человек с похожей веткой", login: "someone", accountType: "User", branch: "renovate/fake", wantCode: 1},
		{name: "бот с чужим префиксом", login: "dependabot[bot]", accountType: "Bot", branch: "renovate/fake", wantCode: 1},
		{name: "бот на обычной ветке", login: "renovate[bot]", accountType: "Bot", branch: "feat/other", wantCode: 1},
		{name: "логин бота с неверным регистром", login: "Renovate[bot]", accountType: "Bot", branch: "renovate/x", wantCode: 1},
		{name: "неполные метаданные без type", login: "renovate[bot]", accountType: "", branch: "renovate/x", wantCode: 1},
		{name: "неполные метаданные без user", login: "", accountType: "", branch: "renovate/x", wantCode: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTemp(t, "event.json", []byte(eventJSON(t, bad, c.login, c.accountType, c.branch)))
			code, stdout, stderr := runCLI("--event-file", path)
			if code != c.wantCode {
				t.Fatalf("код выхода %d, ожидался %d: %q", code, c.wantCode, stderr)
			}
			if c.wantCode == 0 {
				if !strings.Contains(stdout, "формат ручных PR не применяется") {
					t.Fatalf("исключение сработало молча: stdout=%q", stdout)
				}
				if stderr != "" {
					t.Fatalf("неожиданная диагностика: %q", stderr)
				}
			}
		})
	}
}

func TestSenderBotDoesNotExemptHuman(t *testing.T) {
	path := writeTemp(t, "event.json", []byte(eventJSON(t, badBody(), "someone", "User", "feat/x")))
	code, stdout, stderr := runCLI("--event-file", path)
	if code != 1 {
		t.Fatalf("sender-бот освободил PR человека: код %d", code)
	}
	if strings.Contains(stdout, "не применяется") {
		t.Fatalf("исключение сработало по sender: %q", stdout)
	}
	if !strings.Contains(stderr, "## Цель") {
		t.Fatalf("нет диагностики по разделам: %q", stderr)
	}
}

func badBody() string { return "## Цель\nTODO\n" }

func TestModesAgree(t *testing.T) {
	for name, body := range map[string]string{
		"корректный":   validBody(),
		"некорректный": badBody(),
	} {
		t.Run(name, func(t *testing.T) {
			file := writeTemp(t, "body.md", []byte(body))
			event := writeTemp(t, "event.json", []byte(eventJSON(t, body, "someone", "User", "feat/x")))
			fileCode, fileOut, fileErr := runCLI("--body-file", file)
			eventCode, eventOut, eventErr := runCLI("--event-file", event)
			if fileCode != eventCode {
				t.Fatalf("коды различаются: файл %d, событие %d", fileCode, eventCode)
			}
			if fileOut != eventOut || fileErr != eventErr {
				t.Fatalf("вывод различается:\nфайл: %q / %q\nсобытие: %q / %q", fileOut, fileErr, eventOut, eventErr)
			}
		})
	}
}

func TestSuccessfulCLI(t *testing.T) {
	file := writeTemp(t, "body.md", []byte(validBody()))
	code, stdout, stderr := runCLI("--body-file", file)
	if code != 0 {
		t.Fatalf("код выхода %d: %q", code, stderr)
	}
	if !strings.Contains(stdout, "соответствует контракту") {
		t.Fatalf("нет подтверждения успеха: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("неожиданная диагностика: %q", stderr)
	}

	event := writeTemp(t, "event.json", []byte(eventJSON(t, validBody(), "someone", "User", "feat/x")))
	if code, _, stderr := runCLI("--event-file", event); code != 0 {
		t.Fatalf("режим события: код %d: %q", code, stderr)
	}
}

// TestDiagnosticsDoNotEchoBody проверяет, что диагностика не печатает
// недоверенный текст целиком.
func TestDiagnosticsDoNotEchoBody(t *testing.T) {
	secret := "ПОЛНЫЙ-НЕДОВЕРЕННЫЙ-ТЕКСТ"
	body := "## Цель\n" + secret + "\n## Изменения\nTODO\n"
	file := writeTemp(t, "body.md", []byte(body))
	code, stdout, stderr := runCLI("--body-file", file)
	if code != 1 {
		t.Fatalf("код выхода %d, ожидался 1", code)
	}
	if strings.Contains(stderr, secret) || strings.Contains(stdout, secret) {
		t.Fatal("диагностика воспроизводит недоверенный body")
	}
}

// TestExitCodes проверяет коды выхода, которые возвращает точка входа run.
func TestExitCodes(t *testing.T) {
	file := writeTemp(t, "body.md", []byte(badBody()))
	if code, _, _ := runCLI("--body-file", file); code != 1 {
		t.Fatalf("код выхода %d, ожидался 1", code)
	}
	if code, _, _ := runCLI("--body-file", filepath.Join(t.TempDir(), "нет.md")); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
}
