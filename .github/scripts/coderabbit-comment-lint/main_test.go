package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testMarker, testHeader, testDelimiter и строки данных зафиксированы здесь
// буквально, а не выведены из производственных констант: изменение контракта в
// реализации не должно молча подменять вход тестов.
const (
	testMarker    = "<!-- coderabbit-disposition -->"
	testHeader    = "| Итерация / reviewed HEAD | Уровень | Путь | Влияние | Решение | Исправление |"
	testDelimiter = "| --- | --- | --- | --- | --- | --- |"
	testCleanRow  = "| 1 · `aaaaaaa` | — | — | 0 замечаний | clean | `bbbbbbb` |"
	testFinding   = "| 1 · `aaaaaaa` | major | `src/a.go` | потеря данных | confirmed | исправлено в `ccccccc` |"
)

// reportOf собирает отчёт из маркера и таблицы с заданными строками данных.
func reportOf(rows ...string) string {
	parts := []string{testMarker, "", testHeader, testDelimiter}
	return strings.Join(append(parts, strings.Join(rows, "\n")), "\n") + "\n"
}

func validReport() string { return reportOf(testCleanRow) }

// TestContractReferenceIsIndependent фиксирует маркер и заголовок таблицы как
// внешний контракт: производственные константы обязаны совпадать с ним.
func TestContractReferenceIsIndependent(t *testing.T) {
	if commentMarker != testMarker {
		t.Fatalf("маркер контракта изменился: %q, эталон тестов %q", commentMarker, testMarker)
	}
	if got := "| " + strings.Join(tableHeader, " | ") + " |"; got != testHeader {
		t.Fatalf("заголовок таблицы изменился: %q, эталон тестов %q", got, testHeader)
	}
}

func TestValidateCommentAccepts(t *testing.T) {
	secondIteration := "| 2 · `abcdef1` | minor | `src/b.go` | шум | false positive | не правим |"
	sameIteration := "| 1 · `aaaaaaa` | trivial | `src/c.go` | стиль | out of scope | отдельная задача |"
	cases := []struct {
		name   string
		report string
	}{
		{name: "clean-итерация", report: validReport()},
		{name: "несколько findings одной итерации", report: reportOf(testFinding, sameIteration)},
		{name: "несколько итераций", report: reportOf(testFinding, secondIteration)},
		{name: "clean после findings", report: reportOf(testFinding, "| 2 · `abcdef1` | — | — | 0 замечаний | clean | `ccccccc` |")},
		{
			name:   "внешние вертикальные черты",
			report: reportOf("|1 · `aaaaaaa`|major|`a.go`|эффект|confirmed|fix|"),
		},
		{
			name:   "без внешних вертикальных черт",
			report: reportOf("1 · `aaaaaaa` | major | `a.go` | эффект | confirmed | fix"),
		},
		{
			name:   "выравнивание двоеточиями",
			report: strings.Replace(validReport(), testDelimiter, "|:---|:---:|---:|---|---|---|", 1),
		},
		{
			name:   "экранирование в тексте",
			report: reportOf("| 1 · `aaaaaaa` | minor | `a.go` | разметка `a\\|b` | false positive | — |"),
		},
		{
			name:   "экранирование в code span",
			report: reportOf("| 1 · `aaaaaaa` | minor | `a\\|b.md` | разметка | false positive | — |"),
		},
		{
			name:   "полный reviewed SHA",
			report: reportOf("| 1 · `0123456789abcdef0123456789abcdef01234567` | major | `a.go` | эффект | confirmed | fix |"),
		},
		{
			name:   "чистая итерация с маркерным коммитом",
			report: reportOf("| 1 · `aaaaaaa` | — | — | 0 замечаний | clean | `bbbbbbb` |"),
		},
		{
			name:   "проза вокруг таблицы",
			report: "Накопительный журнал ревью.\n\n" + reportOf(testFinding) + "\nИтог: блокирующих замечаний нет.\n",
		},
		{
			name:   "проза с одиночным разделителем после таблицы",
			report: reportOf(testFinding) + "\nКоманда: a | b\n",
		},
		{
			name:   "проза с разделителем в code span после таблицы",
			report: reportOf(testFinding) + "\nИтог: см. `a|b`\n",
		},
		{
			name:   "заголовок второго уровня над таблицей",
			report: "## CodeRabbit review\n\n" + reportOf(testFinding),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validateComment(c.report); len(got) != 0 {
				t.Fatalf("корректный отчёт отклонён: %q", got)
			}
		})
	}
}

// TestLineEndingsNormalized — нормализация переводов строк выполняется один раз
// в начале цепочки, поэтому CRLF и CR не меняют результат.
func TestLineEndingsNormalized(t *testing.T) {
	base := reportOf(testFinding, "| 2 · `abcdef1` | minor | `b.go` | шум | duplicate | см. строку 1 |")
	for name, report := range map[string]string{
		"lf":   base,
		"crlf": strings.ReplaceAll(base, "\n", "\r\n"),
		"cr":   strings.ReplaceAll(base, "\n", "\r"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := validateComment(report); len(got) != 0 {
				t.Fatalf("окончания строк не нормализованы: %q", got)
			}
		})
	}
}

func TestValidateCommentRejects(t *testing.T) {
	cases := []struct {
		name    string
		report  string
		want    string
		line    int // ожидаемый номер строки в диагностике; 0 — не проверяется
		comment string
	}{
		{
			name:   "без маркера",
			report: reportOf(testFinding)[len(testMarker)+1:],
			want:   "Отсутствует маркер",
		},
		{
			name:   "маркер повторяется",
			report: testMarker + "\n" + reportOf(testFinding),
			want:   "встречается 2 раза",
		},
		{
			name:   "маркер не отдельной строкой",
			report: "Текст " + testMarker + "\n" + reportOf(testFinding)[len(testMarker)+1:],
			want:   "должен занимать отдельную строку",
		},
		{
			name:   "маркер в code span",
			report: "`" + testMarker + "`\n" + reportOf(testFinding)[len(testMarker)+1:],
			want:   "должен занимать отдельную строку",
		},
		{
			name:   "маркер внутри HTML-комментария",
			report: "<!--\n" + testMarker + "\n-->\n" + reportOf(testFinding)[len(testMarker)+1:],
			want:   "скрыт",
		},
		{
			name:   "маркер в незакрытом HTML-комментарии",
			report: "<!--\n" + reportOf(testFinding),
			want:   "скрыт",
		},
		{
			name:   "маркер в ограждённом блоке",
			report: "```markdown\n" + reportOf(testFinding) + "```\n",
			want:   "скрыт",
		},
		{
			name:   "маркер в блок-цитате",
			report: "> " + testMarker + "\n\n" + reportOf(testFinding)[len(testMarker)+1:],
			want:   "скрыт",
		},
		{
			name:   "маркер в коде с отступом",
			report: "    " + testMarker + "\n\n" + reportOf(testFinding)[len(testMarker)+1:],
			want:   "скрыт",
		},
		{
			name:   "маркер в raw HTML",
			report: "<div>\n" + testMarker + "\n</div>\n\n" + reportOf(testFinding)[len(testMarker)+1:],
			want:   "скрыт",
		},
		{
			name:   "без таблицы",
			report: testMarker + "\n\nПояснений нет.\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в ограждённом блоке",
			report: testMarker + "\n\n```markdown\n" + strings.Join([]string{testHeader, testDelimiter, testCleanRow}, "\n") + "\n```\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в HTML-комментарии",
			report: testMarker + "\n<!--\n" + strings.Join([]string{testHeader, testDelimiter, testCleanRow}, "\n") + "\n-->\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в raw HTML",
			report: testMarker + "\n<div>\n" + strings.Join([]string{testHeader, testDelimiter, testCleanRow}, "\n") + "\n</div>\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в script",
			report: testMarker + "\n<script>\n" + strings.Join([]string{testHeader, testDelimiter, testCleanRow}, "\n") + "\n</script>\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в блок-цитате",
			report: testMarker + "\n> " + testHeader + "\n> " + testDelimiter + "\n> " + testCleanRow + "\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в коде с отступом",
			report: testMarker + "\n    " + testHeader + "\n    " + testDelimiter + "\n    " + testCleanRow + "\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "таблица в ограждении пункта списка",
			report: testMarker + "\n- пример разметки\n\n  ```text\n  " + testHeader + "\n  " + testDelimiter + "\n  " + testCleanRow + "\n  ```\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "заголовок не совпадает",
			report: testMarker + "\n\n| Итерация | Уровень | Путь | Влияние | Решение | Исправление |\n" + testDelimiter + "\n" + testCleanRow + "\n",
			want:   "Отсутствует заголовок накопительной таблицы",
		},
		{
			name:   "заголовок повторяется",
			report: reportOf(testCleanRow) + "\n" + testHeader + "\n" + testDelimiter + "\n" + testCleanRow + "\n",
			want:   "повторяется",
		},
		{
			name:   "нет разделителя",
			report: testMarker + "\n\n" + testHeader + "\n" + testCleanRow + "\n",
			want:   "нет строки-разделителя",
		},
		{
			name:   "разделитель из пяти ячеек",
			report: testMarker + "\n\n" + testHeader + "\n| --- | --- | --- | --- | --- |\n" + testCleanRow + "\n",
			want:   "нет строки-разделителя",
		},
		{
			name:   "разделитель с посторонней ячейкой",
			report: testMarker + "\n\n" + testHeader + "\n| --- | --- | x | --- | --- | --- |\n" + testCleanRow + "\n",
			want:   "нет строки-разделителя",
		},
		{
			name:   "между заголовком и разделителем текст",
			report: testMarker + "\n\n" + testHeader + "\nПояснение.\n" + testDelimiter + "\n" + testCleanRow + "\n",
			want:   "нет строки-разделителя",
		},
		{
			name:   "нет строк данных",
			report: testMarker + "\n\n" + testHeader + "\n" + testDelimiter + "\n",
			want:   "нет ни одной строки данных",
		},
		{
			name:   "пять столбцов",
			report: reportOf("| 1 · `aaaaaaa` | — | — | clean | `bbbbbbb` |"),
			want:   "содержит 5 столбцов вместо 6",
			line:   5,
		},
		{
			name:   "пустая ячейка",
			report: reportOf("| 1 · `aaaaaaa` | major |  | эффект | confirmed | fix |"),
			want:   "пустую ячейку",
			line:   5,
		},
		{
			name:   "первая ячейка без reviewed HEAD",
			report: reportOf("| 1 | major | `a.go` | эффект | confirmed | fix |"),
			want:   "не связана с итерацией и reviewed HEAD",
			line:   5,
		},
		{
			name:   "номер итерации ноль",
			report: reportOf("| 0 · `aaaaaaa` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "не связана с итерацией и reviewed HEAD",
			line:   5,
		},
		{
			name:   "короткий reviewed SHA",
			report: reportOf("| 1 · `abcdef` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "не связана с итерацией и reviewed HEAD",
			line:   5,
		},
		{
			name:   "reviewed SHA в верхнем регистре",
			report: reportOf("| 1 · `ABCDEF0` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "не связана с итерацией и reviewed HEAD",
			line:   5,
		},
		{
			name:   "reviewed SHA длиннее 40",
			report: reportOf("| 1 · `0123456789abcdef0123456789abcdef012345678` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "не связана с итерацией и reviewed HEAD",
			line:   5,
		},
		{
			name:   "номера итераций с пропуском",
			report: reportOf(testFinding, "| 3 · `abcdef1` | minor | `b.go` | шум | confirmed | fix |"),
			want:   "должны идти последовательно",
			line:   6,
		},
		{
			name:   "номера итераций начинаются не с единицы",
			report: reportOf("| 2 · `aaaaaaa` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "должны идти последовательно",
			line:   5,
		},
		{
			name:   "разные reviewed SHA одной итерации",
			report: reportOf(testFinding, "| 1 · `abcdef1` | minor | `b.go` | шум | confirmed | fix |"),
			want:   "один reviewed HEAD",
			line:   6,
		},
		{
			name: "строки одной итерации не подряд",
			report: reportOf(testFinding,
				"| 2 · `abcdef1` | minor | `b.go` | шум | confirmed | fix |",
				"| 1 · `aaaaaaa` | minor | `c.go` | шум | confirmed | fix |"),
			want: "должны идти подряд",
			line: 7,
		},
		{
			name:   "неканоническое решение",
			report: reportOf("| 1 · `aaaaaaa` | major | `a.go` | эффект | fixed | fix |"),
			want:   "неканоническое значение",
			line:   5,
		},
		{
			name:   "тире в уровне не-clean строки",
			report: reportOf("| 1 · `aaaaaaa` | — | `a.go` | эффект | confirmed | fix |"),
			want:   "допустим только в clean-строке",
			line:   5,
		},
		{
			name:   "тире в пути не-clean строки",
			report: reportOf("| 1 · `aaaaaaa` | major | — | эффект | confirmed | fix |"),
			want:   "допустим только в clean-строке",
			line:   5,
		},
		{
			name:   "clean без тире в уровне",
			report: reportOf("| 1 · `aaaaaaa` | major | — | эффект | clean | `bbbbbbb` |"),
			want:   "clean-строка требует",
			line:   5,
		},
		{
			name:   "clean вместе с finding",
			report: reportOf(testCleanRow, "| 1 · `aaaaaaa` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "clean не может сочетаться",
			line:   6,
		},
		{
			name:   "finding после clean",
			report: reportOf(testCleanRow, "| 1 · `aaaaaaa` | major | `a.go` | эффект | confirmed | fix |"),
			want:   "clean не может сочетаться",
			line:   6,
		},
		{
			name:   "оторванная строка с внешней чертой",
			report: reportOf(testCleanRow) + "\n| 2 · `abcdef1` | minor | `b.go` | шум | confirmed | fix |\n",
			want:   "оторвана от накопительной таблицы",
			line:   7,
		},
		{
			name:   "оторванная строка без внешних черт",
			report: reportOf(testCleanRow) + "\n2 · `abcdef1` | minor | `b.go` | шум | confirmed | fix\n",
			want:   "оторвана от накопительной таблицы",
			line:   7,
		},
		{
			name:   "оторванная строка с повреждённой первой ячейкой",
			report: reportOf(testCleanRow) + "\n| 2 · `bad` | обрыв |\n",
			want:   "оторвана от накопительной таблицы",
			line:   7,
		},
		{
			name:   "незакрытое ограждение скрывает отчёт",
			report: "```text\n" + reportOf(testFinding),
			want:   "скрыт",
		},
		{
			name:   "несовпадающие ограждения скрывают отчёт",
			report: "~~~text\n" + reportOf(testFinding) + "```\n",
			want:   "скрыт",
		},
		{
			name:   "более длинный маркер не закрывается коротким",
			report: "````text\n" + reportOf(testFinding) + "```\n",
			want:   "скрыт",
		},
		{
			name:   "строка информации не закрывает блок",
			report: "```text\n```markdown\n" + reportOf(testFinding) + "```\n",
			want:   "скрыт",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := validateComment(c.report)
			if len(problems) == 0 {
				t.Fatalf("некорректный отчёт принят")
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, c.want) {
				t.Fatalf("нет целевой причины отказа %q: %q", c.want, problems)
			}
			if c.line > 0 {
				want := fmt.Sprintf("Строка %d:", c.line)
				if !strings.Contains(joined, want) {
					t.Fatalf("диагностика без %q: %q", want, problems)
				}
			}
		})
	}
}

// TestLiteralCommentMarker — регрессия: литеральный «<!--» в строчном коде,
// в fenced-блоке и в блоке кода с отступом не открывает HTML-комментарий и не
// скрывает настоящий отчёт, который идёт следом.
func TestLiteralCommentMarker(t *testing.T) {
	target := testMarker + "\n\n" + testHeader + "\n" + testDelimiter + "\n" + testFinding + "\n"
	cases := map[string]string{
		"строчный код":                 "Пример `<!--` в тексте описания.\n\n" + target,
		"строчный код из двух кавычек": "Пример ``<!--`` в тексте описания.\n\n" + target,
		"fenced-блок":                  "```markdown\n<!-- пример\n```\n\n" + target,
		"строка информации блока":      "```text <!--\nпример\n```\n\n" + target,
		"код с отступом":               "Пример разметки:\n\n    <!-- литеральный маркер\n\n" + target,
		"маркер в коде пункта списка":  "- пример\n\n  ```text\n  <!-- литеральный маркер\n  ```\n\n" + target,
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			if got := validateComment(report); len(got) != 0 {
				t.Fatalf("литеральный маркер скрыл отчёт: %q", got)
			}
		})
	}
	// Настоящий комментарий по-прежнему скрывает следующий за ним маркер.
	report := "<!--\n" + target + "-->\n" + target
	if got := validateComment(report); len(got) == 0 {
		t.Fatal("настоящий комментарий не скрыл маркер")
	}
}

// TestEscapedPipe — разделение ячеек только по неэкранированной черте с учётом
// чётности обратных слэшей.
func TestEscapedPipe(t *testing.T) {
	cases := []struct {
		name string
		row  string
		want int
	}{
		{name: "экранированная черта внутри текста", row: "| 1 · `aaaaaaa` | minor | `a.go` | a\\|b | confirmed | fix |", want: 6},
		{name: "две обратные черты перед чертой", row: "| 1 · `aaaaaaa` | minor | `a.go` | a\\\\|b | confirmed | fix |", want: 7},
		{name: "экранированная черта в code span", row: "| 1 · `aaaaaaa` | minor | `a\\|b.go` | эффект | confirmed | fix |", want: 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cells, ok := tableRow(c.row)
			if !ok {
				t.Fatalf("строка не распознана как строка таблицы: %q", c.row)
			}
			if len(cells) != c.want {
				t.Fatalf("ячеек %d, ожидалось %d: %q", len(cells), c.want, cells)
			}
		})
	}
}

// TestHiddenMarkerInCodeInsideList — регрессия: в пункте списка, который сам
// начинается с блока кода, GFM отводит пункту один пробел после маркера, а не
// все. Иначе ожидаемый отступ раздувается, и маркер, скрытый в коде пункта,
// принимается за видимый.
func TestHiddenMarkerInCodeInsideList(t *testing.T) {
	// Пункт «-» + пять пробелов: содержимое пункта начинается с колонки 2,
	// остальное — блок кода, в котором лежит маркер.
	report := "-     пример\n      " + testMarker + "\n\n" +
		testHeader + "\n" + testDelimiter + "\n" + testFinding + "\n"
	problems := validateComment(report)
	if len(problems) == 0 {
		t.Fatal("маркер внутри кода пункта списка принят за видимый")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "скрыт") {
		t.Fatalf("отказ по другой причине: %q", problems)
	}
}

// TestRowWithoutPipeEndsTable — регрессия: строка без «|» сразу после строки
// данных по GFM становится одноклеточной строкой таблицы, а не концом таблицы.
func TestRowWithoutPipeEndsTable(t *testing.T) {
	report := testMarker + "\n\n" + testHeader + "\n" + testDelimiter + "\n" + testFinding + "\nПримечание\n"
	problems := validateComment(report)
	if len(problems) == 0 {
		t.Fatal("одноклеточная строка после данных не отклонена")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "столбц") {
		t.Fatalf("отказ по другой причине: %q", problems)
	}
	// Пустая строка таблицу по-прежнему завершает, проза после неё —
	// самостоятельный блок.
	allowed := testMarker + "\n\n" + testHeader + "\n" + testDelimiter + "\n" + testFinding + "\n\nПримечание\n"
	if got := validateComment(allowed); len(got) != 0 {
		t.Fatalf("проза после пустой строки отклонена: %q", got)
	}
}

// TestHeaderInsideHTMLBlock — регрессия: строка, начинающаяся с HTML-комментария,
// по GFM образует HTML-блок целиком, поэтому заголовок таблицы из неё не
// извлекается.
func TestHeaderInsideHTMLBlock(t *testing.T) {
	report := "<!-- пример -->" + testHeader + "\n" + testDelimiter + "\n" + testFinding + "\n"
	report = testMarker + "\n\n" + report
	problems := validateComment(report)
	if len(problems) == 0 {
		t.Fatal("заголовок из строки HTML-блока принят за настоящий")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "заголовок") {
		t.Fatalf("отказ по другой причине: %q", problems)
	}
}

// TestClosingRawTagWholeName — регрессия: «</prefix>» не закрывает «<pre>»,
// иначе скрытый блок заканчивается раньше времени.
func TestClosingRawTagWholeName(t *testing.T) {
	report := testMarker + "\n<pre>\nстрока </prefix>\n\n" +
		testHeader + "\n" + testDelimiter + "\n" + testFinding + "\n"
	if got := validateComment(report); len(got) == 0 {
		t.Fatal("неверный закрывающий тег завершил raw-блок")
	}
	// Настоящий закрывающий тег блок действительно закрывает.
	closed := testMarker + "\n<pre>\nкод\n</pre>\n\n" +
		testHeader + "\n" + testDelimiter + "\n" + testFinding + "\n"
	if got := validateComment(closed); len(got) != 0 {
		t.Fatalf("настоящий </pre> не закрыл блок: %q", got)
	}
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
	comment := writeTemp(t, "comment.md", []byte(validReport()))
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "без режима", args: nil, wantErr: "ровно один режим"},
		{name: "оба режима", args: []string{"--comment-file", comment, "--event-file", comment}, wantErr: "взаимоисключающие"},
		{name: "неизвестный флаг", args: []string{"--bogus", "x"}, wantErr: "Ошибка использования"},
		{name: "лишний позиционный аргумент", args: []string{"--comment-file", comment, "лишнее"}, wantErr: "лишние позиционные"},
		{name: "флаг без значения", args: []string{"--comment-file"}, wantErr: "Ошибка использования"},
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

func TestCommentInputErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого-файла.md")
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "отсутствующий файл", args: []string{"--comment-file", missing}, wantErr: "Ошибка входа"},
		{
			name:    "некорректный UTF-8",
			args:    []string{"--comment-file", writeTemp(t, "bad.md", []byte{0xff, 0xfe, 0x20})},
			wantErr: "UTF-8",
		},
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

func TestCommentCLI(t *testing.T) {
	t.Run("корректный отчёт", func(t *testing.T) {
		path := writeTemp(t, "comment.md", []byte(validReport()))
		code, stdout, stderr := runCLI("--comment-file", path)
		if code != 0 {
			t.Fatalf("код выхода %d: %q", code, stderr)
		}
		if !strings.Contains(stdout, "соответствует контракту") {
			t.Fatalf("нет подтверждения успеха: %q", stdout)
		}
		if stderr != "" {
			t.Fatalf("неожиданная диагностика: %q", stderr)
		}
	})
	t.Run("нарушение контракта", func(t *testing.T) {
		path := writeTemp(t, "comment.md", []byte(testMarker+"\n\nТаблицы нет.\n"))
		code, stdout, stderr := runCLI("--comment-file", path)
		if code != 1 {
			t.Fatalf("код выхода %d, ожидался 1: %q", code, stderr)
		}
		if !strings.Contains(stderr, "Отсутствует заголовок накопительной таблицы") {
			t.Fatalf("нет целевой диагностики: %q", stderr)
		}
		if stdout != "" {
			t.Fatalf("неожиданный вывод в stdout: %q", stdout)
		}
	})
}

// eventJSON собирает синтетический снимок события pull_request.
func eventJSON(t *testing.T, repository string, number int, sha string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"action":     "synchronize",
		"repository": map[string]any{"full_name": repository},
		"pull_request": map[string]any{
			"number": number,
			"head":   map[string]any{"sha": sha},
			"body":   "устаревшее тело события",
		},
	})
	if err != nil {
		t.Fatalf("не удалось собрать событие: %v", err)
	}
	return payload
}

func TestEventInputErrors(t *testing.T) {
	good := eventJSON(t, "example/project", 7, strings.Repeat("a", 40))
	broken := writeTemp(t, "broken.json", []byte(`{"repository":`))
	noRepository := writeTemp(t, "no-repo.json", []byte(`{"pull_request":{"number":7,"head":{"sha":"`+strings.Repeat("a", 40)+`"}}}`))
	badNumber := writeTemp(t, "bad-number.json", []byte(`{"repository":{"full_name":"example/project"},"pull_request":{"number":0,"head":{"sha":"`+strings.Repeat("a", 40)+`"}}}`))
	shortSHA := writeTemp(t, "short.json", []byte(`{"repository":{"full_name":"example/project"},"pull_request":{"number":7,"head":{"sha":"abc"}}}`))
	badUTF8 := writeTemp(t, "bad-utf8.json", []byte{0xff, 0x00})
	cases := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "отсутствующий файл события",
			args:    []string{"--event-file", filepath.Join(t.TempDir(), "нет.json")},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token"},
			wantErr: "Ошибка входа",
		},
		{
			name:    "некорректный UTF-8 события",
			args:    []string{"--event-file", badUTF8},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token"},
			wantErr: "UTF-8",
		},
		{
			name:    "нет GITHUB_REPOSITORY",
			args:    []string{"--event-file", writeTemp(t, "event.json", good)},
			env:     map[string]string{"GITHUB_TOKEN": "token"},
			wantErr: "GITHUB_REPOSITORY",
		},
		{
			name:    "обход каталога в GITHUB_REPOSITORY",
			args:    []string{"--event-file", writeTemp(t, "event.json", good)},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/../secret", "GITHUB_TOKEN": "token"},
			wantErr: "GITHUB_REPOSITORY",
		},
		{
			name:    "точка как имя репозитория",
			args:    []string{"--event-file", writeTemp(t, "event.json", good)},
			env:     map[string]string{"GITHUB_REPOSITORY": ".", "GITHUB_TOKEN": "token"},
			wantErr: "GITHUB_REPOSITORY",
		},
		{
			name:    "нет GITHUB_TOKEN",
			args:    []string{"--event-file", writeTemp(t, "event.json", good)},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project"},
			wantErr: "GITHUB_TOKEN",
		},
		{
			name:    "токен с управляющим символом",
			args:    []string{"--event-file", writeTemp(t, "event.json", good)},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token\ninjection"},
			wantErr: "GITHUB_TOKEN",
		},
		{
			name:    "некорректный JSON события",
			args:    []string{"--event-file", broken},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token"},
			wantErr: "некорректный JSON",
		},
		{
			name:    "нет объекта repository",
			args:    []string{"--event-file", noRepository},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token"},
			wantErr: "repository",
		},
		{
			name:    "неположительный номер PR",
			args:    []string{"--event-file", badNumber},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token"},
			wantErr: "номера PR",
		},
		{
			name:    "неполный head SHA",
			args:    []string{"--event-file", shortSHA},
			env:     map[string]string{"GITHUB_REPOSITORY": "example/project", "GITHUB_TOKEN": "token"},
			wantErr: "полного head SHA",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runEnv(c.args, &stdout, &stderr, mapGetenv(c.env), nil)
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), c.wantErr) {
				t.Fatalf("в stderr нет %q: %q", c.wantErr, stderr.String())
			}
			if stdout.String() != "" {
				t.Fatalf("неожиданный вывод в stdout: %q", stdout.String())
			}
		})
	}
}

// mapGetenv превращает карту в функцию окружения.
func mapGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// --- сетевой режим: подменяемый транспорт ---

const (
	testRepo     = "example/project"
	testToken    = "secret-token-value"
	testPRNumber = 7
	testSHA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSHAOther = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testBaseSHA  = "cccccccccccccccccccccccccccccccccccccccc"
)

// trackedBody запоминает факт закрытия тела ответа.
type trackedBody struct {
	reader io.Reader
	closed bool
	readAt bool
}

func (b *trackedBody) Read(data []byte) (int, error) {
	b.readAt = true
	return b.reader.Read(data)
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

// failingBody имитирует сбой чтения тела после получения заголовков.
type failingBody struct {
	closed bool
	err    error
}

func (b *failingBody) Read([]byte) (int, error) { return 0, b.err }

func (b *failingBody) Close() error {
	b.closed = true
	return nil
}

// scriptTransport отвечает по сценарию и запоминает фактические запросы.
type scriptTransport struct {
	calls   []string
	methods []string
	headers []http.Header
	bodies  []*trackedBody
	raw     []*failingBody
	handle  func(request *http.Request) (*http.Response, error)
}

func (s *scriptTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.calls = append(s.calls, request.URL.String())
	s.methods = append(s.methods, request.Method)
	s.headers = append(s.headers, request.Header.Clone())
	return s.handle(request)
}

// newTransport создаёт транспорт и подменяемый клиент с запоминанием задержек.
func newTransport(t *testing.T, handle func(*http.Request) (*http.Response, error)) (*scriptTransport, *apiClient, *[]time.Duration) {
	t.Helper()
	transport := &scriptTransport{handle: handle}
	client := newAPIClient(testToken, transport)
	sleeps := []time.Duration{}
	client.sleep = func(delay time.Duration) { sleeps = append(sleeps, delay) }
	return transport, client, &sleeps
}

// trackedResponse собирает ответ API с телом и заголовком Link.
func (s *scriptTransport) trackedResponse(t *testing.T, status int, body, link string) *http.Response {
	t.Helper()
	_ = t
	tracked := &trackedBody{reader: strings.NewReader(body)}
	s.bodies = append(s.bodies, tracked)
	header := http.Header{}
	if link != "" {
		header.Set("Link", link)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       tracked,
		Request:    &http.Request{Method: http.MethodGet},
	}
}

func (s *scriptTransport) pull(t *testing.T, override map[string]any) *http.Response {
	t.Helper()
	return s.trackedResponse(t, http.StatusOK, pullJSON(t, override), "")
}

func (s *scriptTransport) page(t *testing.T, entries []map[string]any, link string) *http.Response {
	t.Helper()
	return s.trackedResponse(t, http.StatusOK, commentsJSON(t, entries...), link)
}

// brokenResponse имитирует сбой чтения тела после получения заголовков.
func (s *scriptTransport) brokenResponse(err error) *http.Response {
	body := &failingBody{err: err}
	s.raw = append(s.raw, body)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body,
		Request: &http.Request{Method: http.MethodGet}}
}

// pullJSON собирает объект PR из карты; карта позволяет менять один фактор.
func pullJSON(t *testing.T, override map[string]any) string {
	t.Helper()
	pull := map[string]any{
		"number": testPRNumber,
		"user":   map[string]any{"id": 1, "login": "author"},
		"head":   map[string]any{"sha": testSHA, "ref": "feature/example"},
		"base":   map[string]any{"sha": testBaseSHA, "ref": "main"},
		"body":   "Описание PR",
	}
	for key, value := range override {
		pull[key] = value
	}
	encoded, err := json.Marshal(pull)
	if err != nil {
		t.Fatalf("не удалось собрать PR: %v", err)
	}
	return string(encoded)
}

// commentsJSON собирает страницу комментариев из готовых записей.
func commentsJSON(t *testing.T, entries ...map[string]any) string {
	t.Helper()
	if entries == nil {
		entries = []map[string]any{}
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("не удалось собрать комментарии: %v", err)
	}
	return string(encoded)
}

func markedComment(id, authorID int64, body string) map[string]any {
	return map[string]any{"id": id, "user": map[string]any{"id": authorID, "login": "author"}, "body": body}
}

func ordinaryComment(id int64, body string) map[string]any {
	return map[string]any{"id": id, "user": nil, "body": body}
}

// pageLink собирает заголовок Link на следующую страницу выдачи комментариев.
func pageLink(page int) string {
	return fmt.Sprintf("<https://api.github.com/repos/%s/issues/%d/comments?per_page=100&page=%d>; rel=\"next\"",
		testRepo, testPRNumber, page)
}

// isPullRequest различает запрос объекта PR и запрос страницы комментариев.
func isPullRequest(request *http.Request) bool {
	return strings.HasPrefix(request.URL.Path, "/repos/"+testRepo+"/pulls/")
}

// pageNumber возвращает запрошенный номер страницы комментариев.
func pageNumber(request *http.Request) int {
	page, err := strconv.Atoi(request.URL.Query().Get("page"))
	if err != nil {
		return 0
	}
	return page
}

// servePages отвечает объектом PR на запрос PR и страницами из pages на запросы
// комментариев; отсутствующая страница отдаётся пустой.
func servePages(t *testing.T, transport *scriptTransport, pages [][]map[string]any, link func(page int) string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(request *http.Request) (*http.Response, error) {
		if isPullRequest(request) {
			return transport.pull(t, nil), nil
		}
		page := pageNumber(request)
		if page < 1 || page > len(pages) {
			return transport.page(t, nil, ""), nil
		}
		return transport.page(t, pages[page-1], link(page)), nil
	}
}

// runEvent запускает сетевой режим на синтетическом снимке события.
func runEvent(t *testing.T, env map[string]string, client *apiClient, event []byte) (int, string, string) {
	t.Helper()
	path := writeTemp(t, "event.json", event)
	var stdout, stderr bytes.Buffer
	code := runEnv([]string{"--event-file", path}, &stdout, &stderr, mapGetenv(env), client)
	return code, stdout.String(), stderr.String()
}

func networkEnv() map[string]string {
	return map[string]string{"GITHUB_REPOSITORY": testRepo, "GITHUB_TOKEN": testToken}
}

func testEvent(t *testing.T) []byte {
	t.Helper()
	return eventJSON(t, testRepo, testPRNumber, testSHA)
}

// TestRequestContract проверяет фактические параметры запросов: метод, доверенный
// host, заголовки и отсутствие произвольного GITHUB_API_URL.
func TestRequestContract(t *testing.T) {
	transport, client, _ := newTransport(t, nil)
	transport.handle = servePages(t, transport, [][]map[string]any{{}}, func(int) string { return "" })
	env := networkEnv()
	env["GITHUB_API_URL"] = "https://attacker.invalid"
	code, _, stderr := runEvent(t, env, client, testEvent(t))
	if code != 0 {
		t.Fatalf("код выхода %d, ожидался 0: %q", code, stderr)
	}
	want := []string{
		"https://api.github.com/repos/example/project/pulls/7",
		"https://api.github.com/repos/example/project/issues/7/comments?per_page=100&page=1",
		"https://api.github.com/repos/example/project/pulls/7",
	}
	if !slices.Equal(transport.calls, want) {
		t.Fatalf("запросы %q, ожидались %q", transport.calls, want)
	}
	for index := range want {
		if transport.methods[index] != http.MethodGet {
			t.Fatalf("запрос %d не GET: %q", index, transport.methods[index])
		}
		header := transport.headers[index]
		for name, expected := range map[string]string{
			"Accept":               "application/vnd.github+json",
			"Authorization":        "Bearer " + testToken,
			"X-GitHub-Api-Version": "2022-11-28",
		} {
			if got := header.Get(name); got != expected {
				t.Fatalf("запрос %d: %s %q, ожидался %q", index, name, got, expected)
			}
		}
		if header.Get("User-Agent") == "" {
			t.Fatalf("запрос %d: пустой User-Agent", index)
		}
	}
}

// TestAbsentCommentIsSuccess — отсутствие служебного комментария после полного
// чтения не является нарушением контракта и не выдаётся за выполненное ревью.
func TestAbsentCommentIsSuccess(t *testing.T) {
	for name, entries := range map[string][]map[string]any{
		"пустая выдача":          nil,
		"немаркированные записи": {ordinaryComment(2, "Обычный комментарий")},
		"немаркированная с null": {map[string]any{"id": 3, "user": nil, "body": "Ещё один"}},
	} {
		t.Run(name, func(t *testing.T) {
			transport, client, _ := newTransport(t, nil)
			transport.handle = servePages(t, transport, [][]map[string]any{entries}, func(int) string { return "" })
			code, stdout, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 0 {
				t.Fatalf("код выхода %d, ожидался 0: %q", code, stderr)
			}
			if !strings.Contains(stdout, "не найден") {
				t.Fatalf("нет сообщения об отсутствии комментария: %q", stdout)
			}
			if stderr != "" {
				t.Fatalf("неожиданная диагностика: %q", stderr)
			}
		})
	}
}

// TestPublishedComment проверяет авторство по numeric ID и вывод нарушений.
func TestPublishedComment(t *testing.T) {
	cases := []struct {
		name     string
		entries  func(t *testing.T) []map[string]any
		wantCode int
		want     string
	}{
		{
			name:     "комментарий автора PR",
			entries:  func(t *testing.T) []map[string]any { return []map[string]any{markedComment(1, 1, validReport())} },
			wantCode: 0,
		},
		{
			name: "обычное сообщение и комментарий автора",
			entries: func(t *testing.T) []map[string]any {
				return []map[string]any{ordinaryComment(2, "Обычный комментарий"), markedComment(3, 1, validReport())}
			},
			wantCode: 0,
		},
		{
			name:     "чужой маркированный комментарий",
			entries:  func(t *testing.T) []map[string]any { return []map[string]any{markedComment(1, 2, validReport())} },
			wantCode: 1,
			want:     "не является автором PR",
		},
		{
			name: "тот же логин при другом numeric ID",
			entries: func(t *testing.T) []map[string]any {
				return []map[string]any{{"id": 1, "user": map[string]any{"id": 2, "login": "author"}, "body": validReport()}}
			},
			wantCode: 1,
			want:     "не является автором PR",
		},
		{
			name: "маркированный комментарий без пользователя",
			entries: func(t *testing.T) []map[string]any {
				return []map[string]any{{"id": 1, "user": nil, "body": validReport()}}
			},
			wantCode: 1,
			want:     "не является автором PR",
		},
		{
			name: "несколько маркированных комментариев",
			entries: func(t *testing.T) []map[string]any {
				return []map[string]any{markedComment(1, 1, validReport()), markedComment(2, 1, validReport())}
			},
			wantCode: 1,
			want:     "Найдено несколько служебных",
		},
		{
			name: "маркер скрыт при корректной таблице",
			entries: func(t *testing.T) []map[string]any {
				body := "<!--\n" + testMarker + "\n-->\n" + strings.Join([]string{testHeader, testDelimiter, testCleanRow}, "\n")
				return []map[string]any{markedComment(1, 1, body)}
			},
			wantCode: 1,
			want:     "скрыт",
		},
		{
			name: "маркер повторяется",
			entries: func(t *testing.T) []map[string]any {
				return []map[string]any{markedComment(1, 1, testMarker+"\n"+validReport())}
			},
			wantCode: 1,
			want:     "встречается 2 раза",
		},
		{
			name: "маркированный комментарий без таблицы",
			entries: func(t *testing.T) []map[string]any {
				return []map[string]any{markedComment(1, 1, testMarker+"\n\nТаблицы нет.\n")}
			},
			wantCode: 1,
			want:     "Отсутствует заголовок накопительной таблицы",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entries := c.entries(t)
			transport, client, _ := newTransport(t, nil)
			transport.handle = servePages(t, transport, [][]map[string]any{entries}, func(int) string { return "" })
			code, stdout, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != c.wantCode {
				t.Fatalf("код выхода %d, ожидался %d: %q", code, c.wantCode, stderr)
			}
			if c.want != "" && !strings.Contains(stderr, c.want) {
				t.Fatalf("в stderr нет %q: %q", c.want, stderr)
			}
			if c.wantCode == 0 && !strings.Contains(stdout, "соответствует контракту") {
				t.Fatalf("нет подтверждения успеха: %q", stdout)
			}
		})
	}
}

// TestPagination проверяет полную пагинацию: короткая страница с next, сто
// записей без Link, дубли на разных страницах и пустую страницу с next.
func TestPagination(t *testing.T) {
	hundred := make([]map[string]any, 0, 100)
	for index := 1; index <= 100; index++ {
		hundred = append(hundred, ordinaryComment(int64(1000+index), fmt.Sprintf("запись %d", index)))
	}
	cases := []struct {
		name      string
		pages     func(t *testing.T) [][]map[string]any
		links     func(page int) string
		wantCode  int
		want      string
		wantCalls int
	}{
		{
			name: "короткая страница со следующей",
			pages: func(t *testing.T) [][]map[string]any {
				return [][]map[string]any{{ordinaryComment(2, "обычный")}, {markedComment(1, 1, validReport())}}
			},
			links: func(page int) string {
				if page == 1 {
					return pageLink(2)
				}
				return ""
			},
			wantCode:  0,
			wantCalls: 4,
		},
		{
			name: "сто записей без Link",
			pages: func(t *testing.T) [][]map[string]any {
				return [][]map[string]any{hundred, nil}
			},
			links:     func(int) string { return "" },
			wantCode:  0,
			wantCalls: 4,
		},
		{
			name: "дубли на разных страницах",
			pages: func(t *testing.T) [][]map[string]any {
				return [][]map[string]any{{markedComment(1, 1, validReport())}, {markedComment(1, 1, validReport())}}
			},
			links: func(page int) string {
				if page == 1 {
					return pageLink(2)
				}
				return ""
			},
			wantCode:  2,
			want:      "повторный ID",
			wantCalls: 3,
		},
		{
			name: "пустая страница со ссылкой продолжения",
			pages: func(t *testing.T) [][]map[string]any {
				return [][]map[string]any{{}}
			},
			links:     func(page int) string { return pageLink(page + 1) },
			wantCode:  2,
			want:      "пустая страница",
			wantCalls: 2,
		},
		{
			name: "каноническая ссылка того же PR",
			pages: func(t *testing.T) [][]map[string]any {
				return [][]map[string]any{{ordinaryComment(2, "первая")}, {}}
			},
			links: func(page int) string {
				if page != 1 {
					return ""
				}
				return fmt.Sprintf("<https://api.github.com/repositories/123/issues/%d/comments?per_page=100&page=%d>; rel=\"next\"",
					testPRNumber, page+1)
			},
			wantCode:  0,
			wantCalls: 4,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pages := c.pages(t)
			transport, client, _ := newTransport(t, nil)
			transport.handle = servePages(t, transport, pages, c.links)
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != c.wantCode {
				t.Fatalf("код выхода %d, ожидался %d: %q", code, c.wantCode, stderr)
			}
			if c.want != "" && !strings.Contains(stderr, c.want) {
				t.Fatalf("в stderr нет %q: %q", c.want, stderr)
			}
			if len(transport.calls) != c.wantCalls {
				t.Fatalf("запросов %d, ожидалось %d: %q", len(transport.calls), c.wantCalls, transport.calls)
			}
			// Следующий запрос строится из доверенного ресурса, а не из Link.
			for _, address := range transport.calls {
				if !strings.HasPrefix(address, "https://api.github.com/repos/"+testRepo+"/") {
					t.Fatalf("запрос ушёл не на исходный ресурс: %q", address)
				}
			}
		})
	}
}

// TestUntrustedLinks — недоверенные и повреждённые заголовки пагинации дают
// отказ без перехода по URL ответа.
func TestUntrustedLinks(t *testing.T) {
	base := fmt.Sprintf("/repos/%s/issues/%d/comments", testRepo, testPRNumber)
	cases := map[string]string{
		"чужой host":             "<https://attacker.invalid" + base + "?per_page=100&page=2>; rel=\"next\"",
		"чужой путь":             "<https://api.github.com/wrong?per_page=100&page=2>; rel=\"next\"",
		"чужой PR":               fmt.Sprintf("<https://api.github.com/repos/%s/issues/8/comments?per_page=100&page=2>; rel=\"next\"", testRepo),
		"канонический нулевой":   fmt.Sprintf("<https://api.github.com/repositories/0/issues/%d/comments?per_page=100&page=2>; rel=\"next\"", testPRNumber),
		"другая страница":        "<https://api.github.com" + base + "?per_page=100&page=3>; rel=\"next\"",
		"другой размер страницы": "<https://api.github.com" + base + "?per_page=50&page=2>; rel=\"next\"",
		"посторонний параметр":   "<https://api.github.com" + base + "?per_page=100&page=2&sort=created>; rel=\"next\"",
		"фрагмент":               "<https://api.github.com" + base + "?per_page=100&page=2#x>; rel=\"next\"",
		"userinfo":               "<https://user@api.github.com" + base + "?per_page=100&page=2>; rel=\"next\"",
		"не HTTPS":               "<http://api.github.com" + base + "?per_page=100&page=2>; rel=\"next\"",
		"повтор rel":             "<https://api.github.com" + base + "?per_page=100&page=2>; rel=\"next\", <https://api.github.com" + base + "?per_page=100&page=2>; rel=\"next\"",
		"повреждённый заголовок": "bad",
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			transport, client, _ := newTransport(t, nil)
			transport.handle = servePages(t, transport, [][]map[string]any{{ordinaryComment(2, "первая")}},
				func(int) string { return link })
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
			}
			if strings.Contains(stderr, "attacker.invalid") {
				t.Fatalf("диагностика воспроизводит недоверенный URL: %q", stderr)
			}
			if len(transport.calls) != 2 {
				t.Fatalf("запросов %d, ожидалось 2: %q", len(transport.calls), transport.calls)
			}
		})
	}
}

// TestMalformedRecords — некорректные записи и страницы fail closed.
func TestMalformedRecords(t *testing.T) {
	cases := map[string]string{
		"страница не массив": `{"id":1}`,
		"запись не объект":   `[1]`,
		"нет id":             `[{"body":"текст"}]`,
		"нулевой id":         `[{"id":0,"body":"текст"}]`,
		"строковый id":       `[{"id":"1","body":"текст"}]`,
		"логический id":      `[{"id":true,"body":"текст"}]`,
		"тело null":          `[{"id":1,"body":null}]`,
		"тело не строка":     `[{"id":1,"body":42}]`,
		"нет тела":           `[{"id":1}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			transport, client, _ := newTransport(t, nil)
			transport.handle = func(request *http.Request) (*http.Response, error) {
				if isPullRequest(request) {
					return transport.pull(t, nil), nil
				}
				return transport.trackedResponse(t, http.StatusOK, body, ""), nil
			}
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
			}
			if !strings.Contains(stderr, "Невозможно достоверно проверить") {
				t.Fatalf("нет диагностики невозможности проверки: %q", stderr)
			}
		})
	}
}

// TestPullValidation — обязательные поля PR, расхождение с событием и автор.
func TestPullValidation(t *testing.T) {
	cases := map[string]map[string]any{
		"другой номер":      {"number": 8},
		"нулевой автор":     {"user": map[string]any{"id": 0, "login": "author"}},
		"строковый автор":   {"user": map[string]any{"id": "1", "login": "author"}},
		"автор null":        {"user": nil},
		"пустой логин":      {"user": map[string]any{"id": 1, "login": ""}},
		"head без SHA":      {"head": map[string]any{"sha": "", "ref": "feature/example"}},
		"head короткий SHA": {"head": map[string]any{"sha": "abc", "ref": "feature/example"}},
		"head без ref":      {"head": map[string]any{"sha": testSHA, "ref": ""}},
		"base без SHA":      {"base": map[string]any{"sha": "", "ref": "main"}},
		"тело не строка":    {"body": 42},
		"HEAD отличается":   {"head": map[string]any{"sha": testSHAOther, "ref": "feature/example"}},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			transport, client, _ := newTransport(t, nil)
			transport.handle = func(request *http.Request) (*http.Response, error) {
				if isPullRequest(request) {
					return transport.pull(t, override), nil
				}
				return transport.page(t, nil, ""), nil
			}
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
			}
		})
	}
	t.Run("тело null допустимо", func(t *testing.T) {
		transport, client, _ := newTransport(t, nil)
		transport.handle = func(request *http.Request) (*http.Response, error) {
			if isPullRequest(request) {
				return transport.pull(t, map[string]any{"body": nil}), nil
			}
			return transport.page(t, nil, ""), nil
		}
		code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
		if code != 0 {
			t.Fatalf("код выхода %d, ожидался 0: %q", code, stderr)
		}
	})
	t.Run("несогласованный репозиторий события", func(t *testing.T) {
		transport, client, _ := newTransport(t, nil)
		transport.handle = func(request *http.Request) (*http.Response, error) {
			return transport.pull(t, nil), nil
		}
		code, _, stderr := runEvent(t, networkEnv(), client, eventJSON(t, "other/project", testPRNumber, testSHA))
		if code != 2 {
			t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
		}
		if len(transport.calls) != 0 {
			t.Fatalf("при несогласованном событии сделаны запросы: %q", transport.calls)
		}
	})
}

// TestPullChangedDuringCheck — изменение PR между чтениями не подтверждает
// смешанный снимок.
func TestPullChangedDuringCheck(t *testing.T) {
	for name, override := range map[string]map[string]any{
		"изменился head":  {"head": map[string]any{"sha": testSHAOther, "ref": "feature/example"}},
		"изменилось тело": {"body": "другое описание"},
		"изменился автор": {"user": map[string]any{"id": 5, "login": "other"}},
	} {
		t.Run(name, func(t *testing.T) {
			pulls := 0
			transport, client, _ := newTransport(t, nil)
			transport.handle = func(request *http.Request) (*http.Response, error) {
				if isPullRequest(request) {
					pulls++
					if pulls == 1 {
						return transport.pull(t, nil), nil
					}
					return transport.pull(t, override), nil
				}
				return transport.page(t, nil, ""), nil
			}
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 2 {
				t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
			}
			if !strings.Contains(stderr, "изменился во время проверки") {
				t.Fatalf("нет целевой причины: %q", stderr)
			}
		})
	}
}

// TestTransport упорядочивает повторы и отказы транспорта.
func TestTransport(t *testing.T) {
	t.Run("временные сбои с конечным повтором", func(t *testing.T) {
		failures := map[string]func(*scriptTransport) (*http.Response, error){
			"ответ 500": func(transport *scriptTransport) (*http.Response, error) {
				return transport.trackedResponse(t, 500, "{}", ""), nil
			},
			"ответ 503": func(transport *scriptTransport) (*http.Response, error) {
				return transport.trackedResponse(t, 503, "{}", ""), nil
			},
			"сбой транспорта": func(*scriptTransport) (*http.Response, error) {
				return nil, errors.New("dial failed https://secret.invalid")
			},
			"таймаут": func(*scriptTransport) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			},
		}
		for name, failure := range failures {
			t.Run(name, func(t *testing.T) {
				transport, client, sleeps := newTransport(t, nil)
				transport.handle = func(request *http.Request) (*http.Response, error) {
					if len(transport.calls) == 1 {
						return failure(transport)
					}
					if isPullRequest(request) {
						return transport.pull(t, nil), nil
					}
					return transport.page(t, nil, ""), nil
				}
				code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
				if code != 0 {
					t.Fatalf("код выхода %d, ожидался 0: %q", code, stderr)
				}
				if len(transport.calls) != 4 {
					t.Fatalf("запросов %d, ожидалось 4: %q", len(transport.calls), transport.calls)
				}
				if len(*sleeps) != 1 {
					t.Fatalf("задержек %d, ожидалась 1: %v", len(*sleeps), *sleeps)
				}
				for _, body := range transport.bodies {
					if !body.closed {
						t.Fatal("тело ответа не закрыто")
					}
				}
			})
		}
	})
	t.Run("ошибка чтения тела повторяется и закрывает ответ", func(t *testing.T) {
		transport, client, sleeps := newTransport(t, nil)
		transport.handle = func(request *http.Request) (*http.Response, error) {
			if len(transport.calls) == 1 {
				return transport.brokenResponse(io.ErrUnexpectedEOF), nil
			}
			if isPullRequest(request) {
				return transport.pull(t, nil), nil
			}
			return transport.page(t, nil, ""), nil
		}
		code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
		if code != 0 {
			t.Fatalf("код выхода %d, ожидался 0: %q", code, stderr)
		}
		for _, body := range transport.raw {
			if !body.closed {
				t.Fatal("тело с ошибкой чтения не закрыто")
			}
		}
		if len(*sleeps) != 1 {
			t.Fatalf("задержек %d, ожидалась 1", len(*sleeps))
		}
	})
	t.Run("постоянный сбой ограничен и безопасен", func(t *testing.T) {
		transport, client, sleeps := newTransport(t, func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed https://secret.invalid with " + testToken)
		})
		code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
		if code != 2 {
			t.Fatalf("код выхода %d, ожидался 2: %q", code, stderr)
		}
		if len(transport.calls) != maxAttempts {
			t.Fatalf("запросов %d, ожидалось %d", len(transport.calls), maxAttempts)
		}
		if len(*sleeps) != maxAttempts-1 {
			t.Fatalf("задержек %d, ожидалось %d", len(*sleeps), maxAttempts-1)
		}
		for _, secret := range []string{"secret.invalid", testToken, "dial failed"} {
			if strings.Contains(stderr, secret) {
				t.Fatalf("диагностика раскрывает %q: %q", secret, stderr)
			}
		}
	})
	t.Run("4xx и redirect без повтора", func(t *testing.T) {
		for _, status := range []int{301, 302, 400, 403, 404, 429} {
			transport, client, sleeps := newTransport(t, nil)
			transport.handle = func(request *http.Request) (*http.Response, error) {
				response := transport.trackedResponse(t, status, "тело-ответа", "")
				response.Header.Set("Location", "https://attacker.invalid/steal")
				return response, nil
			}
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 2 {
				t.Fatalf("статус %d: код выхода %d, ожидался 2", status, code)
			}
			if len(transport.calls) != 1 {
				t.Fatalf("статус %d: запросов %d, ожидался 1", status, len(transport.calls))
			}
			if len(*sleeps) != 0 {
				t.Fatalf("статус %d: неожиданные задержки: %v", status, *sleeps)
			}
			if strings.Contains(stderr, "attacker.invalid") || strings.Contains(stderr, "тело-ответа") {
				t.Fatalf("статус %d: диагностика раскрывает ответ: %q", status, stderr)
			}
			for _, body := range transport.bodies {
				if !body.closed {
					t.Fatalf("статус %d: тело ответа не закрыто", status)
				}
			}
		}
	})
	t.Run("повреждённый JSON и UTF-8 без повтора", func(t *testing.T) {
		for name, body := range map[string]string{
			"некорректный JSON":  "bad-json " + testToken,
			"некорректный UTF-8": "\"\xff " + testToken + "\"",
		} {
			transport, client, sleeps := newTransport(t, nil)
			transport.handle = func(*http.Request) (*http.Response, error) {
				return transport.trackedResponse(t, http.StatusOK, body, ""), nil
			}
			code, _, stderr := runEvent(t, networkEnv(), client, testEvent(t))
			if code != 2 {
				t.Fatalf("%s: код выхода %d, ожидался 2", name, code)
			}
			if len(transport.calls) != 1 {
				t.Fatalf("%s: запросов %d, ожидался 1", name, len(transport.calls))
			}
			if len(*sleeps) != 0 {
				t.Fatalf("%s: неожиданные задержки: %v", name, *sleeps)
			}
			if strings.Contains(stderr, testToken) {
				t.Fatalf("%s: диагностика раскрывает тело: %q", name, stderr)
			}
		}
	})
}

// TestSanitize — управляющие символы не попадают в диагностику.
func TestSanitize(t *testing.T) {
	got := sanitize("до\x01после\nвторая\x7fстрока")
	for _, char := range got {
		if char < 32 || char == 127 {
			t.Fatalf("управляющий символ остался в диагностике: %q", got)
		}
	}
	if !strings.Contains(got, " ") {
		t.Fatalf("управляющие символы не заменены пробелом: %q", got)
	}
}
