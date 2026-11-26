// Программа проверяет накопительный комментарий CodeRabbit: единственный
// служебный маркер и одну связную шестиколоночную таблицу решений в видимом
// Markdown, а в сетевом режиме — ещё и опубликованное состояние PR по GitHub API.
//
// Проверяется форма, а не правдивость содержания: линтер не доказывает, что
// ревью действительно было, что замечания приведены полно и что коммиты
// существуют.
//
// # Документированное подмножество Markdown
//
// Видимость маркера и таблицы определяется только следующими конструкциями;
// всё, что выходит за этот список, считается сомнительной видимостью и даёт
// диагностируемый отказ, а не догадку:
//
//   - ограждённые блоки кода ``` и ~~~ (три и более символов) скрывают текст до
//     согласованного закрытия: тот же символ и не короче открывающего, без
//     строки информации. Блок, открытый в строке пункта списка или на
//     последующей строке с отступом от начала его содержимого, скрывает текст
//     до закрытия или до выхода из пункта; незакрытое и несовпадающее
//     ограждение скрывает остаток документа;
//   - код с отступом от четырёх пробелов, отсчитанных от начала содержимого
//     текущего пункта списка, скрыт;
//   - блок-цитата «>» скрыта;
//   - HTML-блоки GFM: <script>, <pre>, <style> и <textarea> скрывают текст до
//     закрывающего тега (по стандарту его имя не обязано совпадать с
//     открывающим); «<!--», «<?», «<![CDATA[» и «<!» с заглавной латинской
//     буквой скрывают текст до «-->», «?>», «]]>» и «>» соответственно; прочие
//     теги (<div> и подобные) — до пустой строки. Строка, открывшая HTML-блок,
//     разметкой таблицы не является, и остаток строки после завершения блока
//     тоже остаётся скрытым;
//   - HTML-комментарий, открытый в середине видимой строки, скрывает остаток
//     этой строки и следующие строки до «-->». Внутри строчного кода и внутри
//     ограждённого блока литеральный «<!--» комментарий не открывает.
//
// Структура таблицы разбирается по исходной строке: по GFM границы ячеек
// определяются раньше встроенной разметки. Поэтому HTML-комментарий внутри
// строки таблицы не меняет число ячеек и не создаёт канонический заголовок или
// значение удалением фрагмента исходного текста: лишняя неэкранированная черта
// остаётся лишней чертой, а закомментированное содержимое ячейки пустым.
//
// Строка, которая началась внутри скрытого блока, считается скрытой целиком,
// даже если блок закрывается на ней же: неоднозначный случай решается отказом.
//
// Полный CommonMark не реализуется. Вне подмножества остаются: определения
// ссылок и сноски, заголовки setext, таблицы без строки-разделителя,
// многоуровневые и перемежающиеся списки, блоки кода внутри цитаты, встроенный
// HTML внутри ячеек, а также любые конструкции, где видимость зависит от
// вложенности контейнеров глубже одноуровневого пункта списка. Если такая
// конструкция делает видимость неоднозначной, линтер отказывает, а не угадывает:
// маркер или заголовок, не попавший в видимый слой, даёт диагностику
// «Отсутствует маркер» или «Отсутствует заголовок накопительной таблицы».
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	usageLine = "Использование: coderabbit-comment-lint (--comment-file <путь> | --event-file <путь>)"

	// commentMarker — служебный маркер отчёта: он встречается ровно один раз,
	// занимает отдельную строку и должен быть виден.
	commentMarker = "<!-- coderabbit-disposition -->"

	// htmlCommentStart открывает HTML-комментарий; строка, начинающаяся с него,
	// по GFM является HTML-блоком целиком.
	htmlCommentStart = "<!--"

	// cdataStart открывает блок CDATA — отдельный вид HTML-блока GFM.
	cdataStart = "<![CDATA["

	// emptyValue заполняет «Уровень» и «Путь» в clean-строке и допустим только
	// там.
	emptyValue      = "—"
	cleanDecision   = "clean"
	tableColumns    = 6
	apiHost         = "api.github.com"
	apiBase         = "https://" + apiHost
	apiAccept       = "application/vnd.github+json"
	apiVersion      = "2022-11-28"
	apiUserAgent    = "coderabbit-comment-lint"
	pageSize        = 100
	maxPages        = 1000
	maxResponseSize = 8 << 20
	maxAttempts     = 3
	requestTimeout  = 30 * time.Second
	retryUnit       = 100 * time.Millisecond
)

// tableHeader — точный заголовок накопительной таблицы; порядок обязателен.
var tableHeader = []string{"Итерация / reviewed HEAD", "Уровень", "Путь", "Влияние", "Решение", "Исправление"}

// canonicalDecisions — канонические решения в порядке шкалы: единственный
// источник и для проверки, и для диагностики.
var canonicalDecisions = []string{
	"confirmed",
	"partially confirmed",
	"false positive",
	"stale",
	"duplicate",
	"out of scope",
	"unconfirmed",
	cleanDecision,
}

// canonicalSeverities — шкала уровня критичности провайдера без переименования: «Уровень»
// принимает только эти значения, кроме clean-строки с «—». Тот же список служит
// диагностикой.
var canonicalSeverities = []string{"critical", "major", "minor", "trivial", "info", "none"}

// decisionSet и severitySet — проверочные множества, построенные из тех же
// списков: отдельной копии набора нет.
var (
	decisionSet = valueSet(canonicalDecisions)
	severitySet = valueSet(canonicalSeverities)
)

// valueSet строит множество допустимых значений из упорядоченного списка.
func valueSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// valueList печатает упорядоченный список допустимых значений для диагностики.
func valueList(values []string) string {
	return strings.Join(values, ", ")
}

var (
	// delimiterCell — ячейка строки-разделителя GFM: дефисы с необязательным
	// выравниванием.
	delimiterCell = regexp.MustCompile(`^:?-+:?$`)
	// iterationCell — первая ячейка: номер итерации, «·» и reviewed HEAD в code
	// span из 7–40 строчных hex-символов.
	iterationCell = regexp.MustCompile("^([1-9][0-9]*)\\s*·\\s*`([0-9a-f]{7,40})`$")
	// iterationPrefix опознаёт вероятное продолжение таблицы даже при
	// повреждённом reviewed HEAD.
	iterationPrefix = regexp.MustCompile(`^[1-9][0-9]*\s*·`)
	// hexSHA — полный установленный SHA ревизии.
	hexSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repositoryID = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	linkItem     = regexp.MustCompile(`^\s*<([^<>]+)>\s*;\s*rel="([a-z]+)"\s*$`)
	// singleTag — одиночный полный тег, занимающий строку целиком (HTML-блок
	// вида 7 по GFM): имя, необязательные атрибуты и закрывающая скобка.
	singleTag = regexp.MustCompile(`^</?[A-Za-z][A-Za-z0-9-]*(\s[^<>]*)?/?>\s*$`)
)

// rawTextTags скрывают текст до своего закрывающего тега, а не до пустой строки.
var rawTextTags = map[string]bool{"script": true, "pre": true, "textarea": true, "style": true}

// htmlEndTags — закрывающие теги, завершающие блок raw-текста. По GFM имя
// закрывающего тега не обязано совпадать с открывающим.
var htmlEndTags = []string{"</script>", "</pre>", "</textarea>", "</style>"}

// htmlBlockEnd — конечные строки остальных видов HTML-блока.
var htmlBlockEnd = map[htmlKind]string{
	htmlInstruction: "?>",
	htmlDeclaration: ">",
	htmlCData:       "]]>",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run — рабочая точка входа: переменные окружения берутся из os.Getenv, транспорт
// по умолчанию. Подмена окружения и транспорта остаётся только в тестах.
func run(args []string, stdout, stderr io.Writer) int {
	return runEnv(args, stdout, stderr, os.Getenv, nil)
}

// runEnv разбирает аргументы и вход, затем печатает диагностику.
// Коды выхода: 0 — комментарий соответствует контракту; 1 — нарушения
// контракта; 2 — ошибка использования, входа или невозможность достоверной
// проверки.
func runEnv(args []string, stdout, stderr io.Writer, getenv func(string) string, client *apiClient) int {
	flags := flag.NewFlagSet("coderabbit-comment-lint", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	commentFile := flags.String("comment-file", "", "локальный Markdown-файл с накопительным комментарием")
	eventFile := flags.String("event-file", "", "JSON-снимок события GitHub pull_request")
	if err := flags.Parse(args); err != nil {
		// Сообщение пакета flag английское: заменяем его своим.
		return usageError(stderr, "неизвестный флаг или отсутствует его значение")
	}
	if flags.NArg() > 0 {
		return usageError(stderr, "лишние позиционные аргументы")
	}
	// Режим устанавливается фактом передачи флага, а не значением пути: пустое
	// значение — ошибка использования, и второй режим не подменяет первый.
	given := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { given[item.Name] = true })
	switch {
	case given["comment-file"] && given["event-file"]:
		return usageError(stderr, "флаги --comment-file и --event-file взаимоисключающие")
	case !given["comment-file"] && !given["event-file"]:
		return usageError(stderr, "нужен ровно один режим: --comment-file или --event-file")
	case given["comment-file"] && *commentFile == "":
		return usageError(stderr, "пустой --comment-file: нужен непустой путь")
	case given["event-file"] && *eventFile == "":
		return usageError(stderr, "пустой --event-file: нужен непустой путь")
	}
	if *commentFile != "" {
		body, err := readTextFile(*commentFile)
		if err != nil {
			return inputError(stderr, err)
		}
		return report(validateComment(body), stdout, stderr)
	}
	// Файл события — локальный вход: его ошибки отделены от сетевых.
	event, err := readEvent(*eventFile)
	if err != nil {
		return inputError(stderr, err)
	}
	problems, found, err := checkPublished(getenv, client, event)
	if err != nil {
		return checkError(stderr, err)
	}
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintln(stderr, sanitize(problem))
		}
		return 1
	}
	if found {
		fmt.Fprintln(stdout, "Опубликованный комментарий CodeRabbit соответствует контракту")
	} else {
		fmt.Fprintln(stdout, "Служебный комментарий CodeRabbit не найден: комментарии PR прочитаны полностью")
	}
	return 0
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "Ошибка использования: %s\n", sanitize(message))
	fmt.Fprintln(stderr, usageLine)
	return 2
}

func inputError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "Ошибка входа: %s\n", sanitize(err.Error()))
	return 2
}

// checkError сообщает, что достоверная проверка невозможна: неполное чтение,
// изменённый PR, сбой транспорта или некорректный ответ API.
func checkError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "Невозможно достоверно проверить: %s\n", sanitize(err.Error()))
	return 2
}

// report печатает список нарушений контракта; пустой список — успех.
func report(problems []string, stdout, stderr io.Writer) int {
	if len(problems) == 0 {
		fmt.Fprintln(stdout, "Комментарий CodeRabbit соответствует контракту")
		return 0
	}
	for _, problem := range problems {
		fmt.Fprintln(stderr, sanitize(problem))
	}
	return 1
}

// sanitize заменяет управляющие символы пробелом: данные недоверенного
// комментария или события не должны формировать управляющие строки рабочего процесса.
func sanitize(message string) string {
	var builder strings.Builder
	for _, char := range message {
		if char < 32 || char == 127 {
			builder.WriteRune(' ')
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// readTextFile читает файл и отвергает некорректный UTF-8. Исходная ошибка ОС
// сохраняется: её нельзя терять и нельзя выдавать за нарушение контракта.
func readTextFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать файл %s; техническая причина: %w", path, err)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("файл %s содержит некорректный UTF-8", path)
	}
	return string(data), nil
}

// normalizeNewlines — единственный владелец нормализации переводов строк в
// цепочке проверки: разбор видимости и таблицы обязан видеть один и тот же
// текст, поэтому нормализация выполняется ровно один раз в начале.
func normalizeNewlines(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return strings.ReplaceAll(body, "\r", "\n")
}

// validateComment возвращает детерминированный список нарушений контракта;
// пустой список означает, что форма отчёта соответствует контракту.
func validateComment(body string) []string {
	lines := strings.Split(normalizeNewlines(body), "\n")
	view := visibleLines(lines)
	problems := checkMarker(lines, view)
	return append(problems, checkTable(view)...)
}

// checkMarker проверяет, что маркер встречается ровно один раз, занимает
// отдельную строку и виден.
func checkMarker(lines []string, view []visibleLine) []string {
	count := strings.Count(strings.Join(lines, "\n"), commentMarker)
	if count == 0 {
		return []string{"Отсутствует маркер " + commentMarker}
	}
	if count > 1 {
		return []string{fmt.Sprintf(
			"Маркер %s встречается %d раза: нужен ровно один", commentMarker, count)}
	}
	index := 0
	for position, line := range lines {
		if strings.Contains(line, commentMarker) {
			index = position
			break
		}
	}
	// Скрытость проверяется первой: строка внутри скрытого блока уже не является
	// отдельной видимой строкой, и сообщать об этом точнее.
	if view[index].hidden {
		return []string{"Маркер " + commentMarker + " скрыт внутри комментария, кода, цитаты или необработанного HTML"}
	}
	if strings.TrimSpace(lines[index]) != commentMarker {
		return []string{"Маркер " + commentMarker + " должен занимать отдельную строку"}
	}
	return nil
}

// visibleLine — результат разбора одной строки: исходный текст видимой строки
// либо признак, что строка скрыта. Для таблицы важен именно исходный текст:
// по GFM границы ячеек определяются раньше разбора встроенной разметки, поэтому
// HTML-комментарий внутри строки не может ни спрятать, ни создать границу.
type visibleLine struct {
	text   string
	hidden bool
}

// fence описывает активный ограждённый блок.
type fence struct {
	ch  byte // маркер ограждения: '`' или '~'
	n   int  // длина маркера открытия
	col int  // 0 — блок верхнего уровня; >0 — блок внутри пункта списка
}

// htmlKind — вид активного HTML-блока по GFM. Виды 1–5 завершаются своей
// конечной строкой, виды 6 и 7 — пустой строкой.
type htmlKind int

const (
	htmlNone        htmlKind = 0
	htmlRawText     htmlKind = 1 // <script>, <pre>, <style>, <textarea>
	htmlComment     htmlKind = 2 // <!--
	htmlInstruction htmlKind = 3 // <?
	htmlDeclaration htmlKind = 4 // <! и заглавная латинская буква
	htmlCData       htmlKind = 5 // <![CDATA[
	htmlBlockTag    htmlKind = 6 // известный блочный тег
	htmlAnyTag      htmlKind = 7 // одиночный полный тег
)

// viewer отслеживает состояние видимости по строкам документа.
type viewer struct {
	fence   *fence
	comment bool
	html    htmlKind
	// htmlCol — столбец содержимого пункта списка, внутри которого открыт
	// HTML-блок; ноль — блок вне списка. Пункт заканчивается на строке с
	// меньшим отступом, и тогда блок тоже заканчивается.
	htmlCol int
	listCol int
}

// visibleLines размечает строки документа: скрытые блоки заменяются признаком
// hidden, остальные получают исходный текст. Строка, начавшаяся внутри скрытого
// блока, считается скрытой целиком, даже если блок закрывается на ней же: так
// неоднозначный случай решается отказом, а не догадкой.
func visibleLines(lines []string) []visibleLine {
	state := &viewer{}
	view := make([]visibleLine, len(lines))
	for index, line := range lines {
		view[index] = state.line(line)
	}
	return view
}

func (v *viewer) line(line string) visibleLine {
	if v.fence != nil {
		active := *v.fence
		if active.col > 0 && !blank(line) && indentWidth(line) < active.col {
			// Пункт списка закончился раньше закрывающего маркера.
			v.fence = nil
			return v.line(line)
		}
		if closesFence(line, active) {
			v.fence = nil
		}
		return visibleLine{hidden: true}
	}
	if v.html != htmlNone {
		// HTML-блок, открытый внутри пункта списка, заканчивается вместе с
		// пунктом: строка меньшего отступа уже вне контейнера и разбирается
		// заново. Без этого маркер после «- <div>note</div>» считался бы
		// скрытым, хотя GFM рисует его отдельным блоком.
		if v.htmlCol > 0 && !blank(line) && indentWidth(line) < v.htmlCol {
			v.html = htmlNone
			v.htmlCol = 0
			return v.line(line)
		}
		if closesHTMLBlock(line, v.html) {
			v.html = htmlNone
			v.htmlCol = 0
		}
		return visibleLine{hidden: true}
	}
	if v.comment {
		if !blank(line) {
			// Строчный комментарий живёт внутри своего абзаца. Ограждение,
			// заголовок, цитата, пункт списка или HTML-блок прерывают абзац и
			// начинают новый блок, поэтому комментарий на них не
			// распространяется: иначе «Note <!--» съедал бы открывающее «~~~»
			// и таблица внутри кода принималась бы за настоящую.
			if startsNewBlock(line) {
				v.comment = false
				return v.line(line)
			}
			if strings.Contains(line, "-->") {
				v.comment = false
			}
			return visibleLine{hidden: true}
		}
		// Строчный комментарий не переходит через пустую строку: абзац
		// закончился, следующий блок разбирается заново. Незакрытый «<!--»
		// в конце абзаца — литеральный текст, а не начало скрытого блока
		// (GFM скрывает только комментарий, закрытый в том же блоке).
		v.comment = false
	}
	if blank(line) {
		// Пустая строка может продолжать пункт списка, поэтому listCol
		// сохраняется; непустая строка меньшего отступа пункт завершает.
		return visibleLine{}
	}
	if indentWidth(line) < v.listCol {
		v.listCol = 0
	}
	if col, ok := listItemContent(line); ok {
		v.listCol = col
	}
	// Код с отступом отсчитывается от начала содержимого текущего пункта
	// списка: вне списка это четыре пробела, внутри пункта отступ продолжения
	// кодом не является.
	if indentWidth(line) >= v.listCol+4 {
		return visibleLine{hidden: true}
	}
	if opening, ok := openFence(line, v.listCol); ok {
		v.fence = &opening
		return visibleLine{hidden: true}
	}
	if opening, ok := openListFence(line); ok {
		v.fence = &opening
		return visibleLine{hidden: true}
	}
	if quoted(line) {
		return visibleLine{hidden: true}
	}
	if kind := htmlBlockStartIn(line, v.listCol); kind != htmlNone {
		if !closesHTMLBlock(line, kind) {
			v.html = kind
			v.htmlCol = v.listCol
			return visibleLine{hidden: true}
		}
		// Блок из одной строки. Отдельный HTML-комментарий — допустимое место
		// служебного маркера, поэтому строка остаётся пустой, но не разметкой
		// таблицы; любой другой остаток строки — уже не Markdown.
		if kind == htmlComment && strings.TrimSpace(stripComments(line, &v.comment)) == "" {
			return visibleLine{}
		}
		return visibleLine{hidden: true}
	}
	// HTML-комментарий, открытый в середине видимой строки, не меняет её
	// разметку: по GFM границы ячеек таблицы определяются раньше встроенной
	// разметки. Учитывается только состояние комментария для следующих строк;
	// содержимое комментария внутри ячейки скрывается при проверке непустоты.
	stripComments(line, &v.comment)
	return visibleLine{text: line}
}

// blockTags — имена тегов, открывающих HTML-блок вида 6 по GFM.
var blockTags = []string{
	"address", "article", "aside", "base", "basefont", "blockquote", "body",
	"caption", "center", "col", "colgroup", "dd", "details", "dialog", "dir",
	"div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form",
	"frame", "frameset", "h1", "h2", "h3", "h4", "h5", "h6", "head", "header",
	"hr", "html", "iframe", "legend", "li", "link", "main", "menu", "menuitem",
	"nav", "noframes", "ol", "optgroup", "option", "p", "param", "section",
	"source", "summary", "table", "tbody", "td", "tfoot", "th", "thead", "title",
	"tr", "track", "ul",
}

// htmlBlockStart распознаёт строку, открывающую HTML-блок GFM верхнего уровня;
// htmlNone — строка HTML-блок не открывает. Учитывается не более трёх пробелов
// отступа.
func htmlBlockStart(line string) htmlKind {
	pad := leadingSpaces(line)
	if pad > 3 {
		return htmlNone
	}
	return htmlBlockStartText(line[pad:])
}

// htmlBlockStartIn распознаёт открытие HTML-блока внутри текущего контейнера.
// В пункте списка содержимое начинается сразу за маркером, поэтому «- <pre>»
// открывает raw-блок, который GitHub рисует как скрытый код; пропуск этого
// случая принимал спрятанную в нём таблицу за настоящую. Вне пункта блок
// начинается с отступом не более трёх пробелов от столбца содержимого listCol.
func htmlBlockStartIn(line string, listCol int) htmlKind {
	if col, ok := listItemContent(line); ok {
		return htmlBlockStartText(line[col:])
	}
	pad := leadingSpaces(line)
	if pad < listCol || pad > listCol+3 {
		return htmlNone
	}
	return htmlBlockStartText(line[pad:])
}

// htmlBlockStartText распознаёт HTML-блок в тексте, уже освобождённом от
// отступа контейнера.
func htmlBlockStartText(rest string) htmlKind {
	lower := strings.ToLower(rest)
	for tag := range rawTextTags {
		if hasTagPrefix(lower, "<"+tag, false) {
			return htmlRawText
		}
	}
	switch {
	case strings.HasPrefix(rest, cdataStart):
		return htmlCData
	case strings.HasPrefix(rest, "<?"):
		return htmlInstruction
	case !strings.HasPrefix(rest, htmlCommentStart) && startsDeclaration(rest):
		return htmlDeclaration
	case strings.HasPrefix(rest, htmlCommentStart):
		return htmlComment
	}
	for _, tag := range blockTags {
		if hasTagPrefix(lower, "<"+tag, true) || hasTagPrefix(lower, "</"+tag, true) {
			return htmlBlockTag
		}
	}
	if singleTag.MatchString(rest) {
		return htmlAnyTag
	}
	return htmlNone
}

// startsDeclaration сообщает, что строка открывает объявление GFM: «<!» и
// заглавная латинская буква.
func startsDeclaration(rest string) bool {
	return len(rest) > 2 && rest[0] == '<' && rest[1] == '!' && rest[2] >= 'A' && rest[2] <= 'Z'
}

// hasTagPrefix сообщает, что текст начинается с имени тега и за именем сразу
// идёт допустимый разделитель: пробел, табуляция, конец строки или «>». Форма
// «/>» допустима только для блочных тегов, а не для raw-текста. Так «<pre» не
// путается с «<prefix>».
func hasTagPrefix(lower, prefix string, selfClosing bool) bool {
	if !strings.HasPrefix(lower, prefix) {
		return false
	}
	tail := lower[len(prefix):]
	if tail == "" {
		return true
	}
	switch tail[0] {
	case ' ', '\t', '>':
		return true
	case '/':
		return selfClosing && len(tail) > 1 && tail[1] == '>'
	}
	return false
}

// closesHTMLBlock сообщает, что строка удовлетворяет конечному условию блока.
func closesHTMLBlock(line string, kind htmlKind) bool {
	switch kind {
	case htmlRawText:
		lower := strings.ToLower(line)
		for _, tag := range htmlEndTags {
			if strings.Contains(lower, tag) {
				return true
			}
		}
		return false
	case htmlBlockTag, htmlAnyTag:
		return blank(line)
	case htmlComment:
		return strings.Contains(line, "-->")
	}
	end, ok := htmlBlockEnd[kind]
	return ok && strings.Contains(line, end)
}

// stripComments убирает HTML-комментарии, включая многострочные и незакрытые:
// незакрытый комментарий скрывает остаток строки и следующие строки. Строчный
// код в обратных кавычках — литеральный контекст: «<!--» внутри него
// комментарий не открывает. Вызывается вне ограждённых блоков кода и блоков с
// отступом, поэтому маркер в коде остаётся литералом.
func stripComments(line string, inside *bool) string {
	var visible strings.Builder
	rest := line
	for {
		if *inside {
			end := strings.Index(rest, "-->")
			if end < 0 {
				return visible.String()
			}
			*inside = false
			rest = rest[end+3:]
			continue
		}
		start := strings.Index(rest, "<!--")
		if tick := strings.IndexByte(rest, '`'); tick >= 0 && (start < 0 || tick < start) {
			if end := codeSpanEnd(rest, tick); end > 0 {
				visible.WriteString(rest[:end])
				rest = rest[end:]
				continue
			}
		}
		if start < 0 {
			visible.WriteString(rest)
			return visible.String()
		}
		visible.WriteString(rest[:start])
		*inside = true
		rest = rest[start+4:]
	}
}

// codeSpanEnd возвращает позицию сразу после закрывающего маркера строчного
// кода, начатого в позиции start, либо -1, если маркер не закрыт.
func codeSpanEnd(text string, start int) int {
	length := 0
	for start+length < len(text) && text[start+length] == '`' {
		length++
	}
	from := start + length
	for {
		index := strings.Index(text[from:], strings.Repeat("`", length))
		if index < 0 {
			return -1
		}
		end := from + index
		next := end
		for next < len(text) && text[next] == '`' {
			next++
		}
		if next-end == length {
			return next
		}
		from = next
	}
}

func blank(line string) bool {
	return strings.TrimSpace(line) == ""
}

// indentWidth считает ширину ведущего отступа; табуляция приблизительно равна
// четырём пробелам, поэтому строка с ведущей табуляцией считается код-блоком.
func indentWidth(line string) int {
	width := 0
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case ' ':
			width++
		case '\t':
			width += 4
		default:
			return width
		}
	}
	return width
}

func leadingSpaces(line string) int {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	return spaces
}

// listItemContent возвращает столбец начала содержимого пункта списка
// одноуровневой разметки; второй результат — является ли строка таким пунктом.
//
// Если пункт начинается с блока кода с отступом (пять и более пробелов после
// маркера), GFM отводит пункту ровно один пробел, а остаток считает кодом:
// иначе ожидаемый отступ раздувается и скрытый в коде текст принимается за
// видимый.
func listItemContent(line string) (int, bool) {
	pad := leadingSpaces(line)
	if pad > 3 {
		return 0, false
	}
	marker := listMarkerLen(line[pad:])
	if marker == 0 {
		return 0, false
	}
	spaces := 0
	for pad+marker+spaces < len(line) && (line[pad+marker+spaces] == ' ' || line[pad+marker+spaces] == '\t') {
		spaces++
	}
	if spaces >= 5 {
		return pad + marker + 1, true
	}
	return pad + marker + spaces, true
}

// listMarkerLen возвращает длину маркера пункта списка в начале текста:
// `- `, `* `, `+ `, `1. `, `1) `; ноль — маркера нет.
func listMarkerLen(text string) int {
	if len(text) >= 1 && (text[0] == '-' || text[0] == '*' || text[0] == '+') &&
		(len(text) == 1 || text[1] == ' ' || text[1] == '\t') {
		return 1
	}
	digits := 0
	for digits < len(text) && text[digits] >= '0' && text[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits > 9 || digits >= len(text) || (text[digits] != '.' && text[digits] != ')') {
		return 0
	}
	if digits+1 < len(text) && text[digits+1] != ' ' && text[digits+1] != '\t' {
		return 0
	}
	return digits + 1
}

// openFence распознаёт открывающий маркер ограждённого блока. Допустимый отступ
// отсчитывается от начала содержимого текущего пункта списка, если блок ему
// принадлежит.
func openFence(line string, listCol int) (fence, bool) {
	pad := leadingSpaces(line)
	base := 0
	if listCol > 0 && pad >= listCol {
		base = listCol
	}
	if pad-base > 3 {
		return fence{}, false
	}
	ch, length, ok := fenceMarker(line[pad:])
	if !ok {
		return fence{}, false
	}
	if base > 0 {
		return fence{ch: ch, n: length, col: base}, true
	}
	return fence{ch: ch, n: length}, true
}

// openListFence распознаёт ограждённый блок, начатый в той же строке, что и
// пункт списка.
func openListFence(line string) (fence, bool) {
	col, ok := listItemContent(line)
	if !ok {
		return fence{}, false
	}
	ch, length, ok := fenceMarker(line[col:])
	if !ok {
		return fence{}, false
	}
	return fence{ch: ch, n: length, col: col}, true
}

// fenceMarker возвращает символ и длину маркера ограждения не короче трёх
// символов. Строка информации о блоке в обратных кавычках не может содержать `.
func fenceMarker(text string) (byte, int, bool) {
	if len(text) < 3 || (text[0] != '`' && text[0] != '~') {
		return 0, 0, false
	}
	ch := text[0]
	length := 0
	for length < len(text) && text[length] == ch {
		length++
	}
	if length < 3 {
		return 0, 0, false
	}
	if ch == '`' && strings.IndexByte(text[length:], '`') >= 0 {
		return 0, 0, false
	}
	return ch, length, true
}

// closesFence проверяет закрывающий маркер: тот же символ, не короче
// открывающего, без строки информации о блоке.
func closesFence(line string, active fence) bool {
	text := line
	if active.col > 0 {
		if len(line) < active.col || strings.TrimSpace(line[:active.col]) != "" {
			return false
		}
		text = line[active.col:]
	}
	pad := leadingSpaces(text)
	if pad > 3 {
		return false
	}
	text = text[pad:]
	length := 0
	for length < len(text) && text[length] == active.ch {
		length++
	}
	if length < active.n {
		return false
	}
	return strings.Trim(text[length:], " \t") == ""
}

// quoted сообщает, что строка продолжает блок-цитату: её текст невидим для
// контракта.
func quoted(line string) bool {
	pad := leadingSpaces(line)
	return pad < 4 && pad < len(line) && line[pad] == '>'
}

// tableRow разбирает строку таблицы GFM; второй результат — строка вообще может
// быть строкой таблицы. Пустая строка завершает таблицу, отступ от четырёх
// пробелов делает строку кодом, внешние «|» необязательны.
func tableRow(text string) ([]string, bool) {
	if blank(text) || indentWidth(text) > 3 || !hasUnescapedPipe(text) {
		return nil, false
	}
	cells := splitCells(text)
	if len(cells) == 0 {
		return nil, false
	}
	return cells, true
}

// hasUnescapedPipe сообщает, что в строке есть «|», не экранированная
// обратным слэшем.
func hasUnescapedPipe(text string) bool {
	return len(splitUnescapedPipes(text)) > 1
}

// splitUnescapedPipes делит строку по «|», которая не является разделителем
// ячейки. По фактическому поведению GFM (сверено с рендером GitHub для одного,
// двух и трёх обратных слэшей) черта литеральна, если непосредственно перед ней
// стоит обратный слэш; чётность серии слэшей роли не играет. Правило чётности
// давало лишнее разделение: «a\\|b» в отчёте считалось бы двумя ячейками, хотя
// GitHub рисует одну.
func splitUnescapedPipes(text string) []string {
	var parts []string
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] != '|' {
			continue
		}
		if index > 0 && text[index-1] == '\\' {
			continue
		}
		parts = append(parts, text[start:index])
		start = index + 1
	}
	return append(parts, text[start:])
}

// splitCells возвращает ячейки строки таблицы: внешние «|» необязательны,
// экранирование действует только на разделение, содержимое ячейки не
// раскавычивается.
func splitCells(text string) []string {
	parts := splitUnescapedPipes(text)
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func sameCells(cells, wanted []string) bool {
	if len(cells) != len(wanted) {
		return false
	}
	for index := range wanted {
		if cells[index] != wanted[index] {
			return false
		}
	}
	return true
}

// startsNewBlock сообщает, что GFM начинает на этой строке другой блок и тем
// самым завершает таблицу. Проверяются только конструкции, документированные в
// шапке файла; обычная проза (в том числе с одиночной «|») таблицу не
// продолжает, а становится её одноклеточной строкой.
func startsNewBlock(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	// Заголовок ATX: «#» … «######» и пробел.
	run := 0
	for run < len(trimmed) && trimmed[run] == '#' {
		run++
	}
	if run >= 1 && run <= 6 && run < len(trimmed) && (trimmed[run] == ' ' || trimmed[run] == '\t') {
		return true
	}
	// Блок-цитата, ограждение, пункт списка — отдельные блоки.
	if quoted(trimmed) {
		return true
	}
	if _, _, ok := fenceMarker(trimmed); ok {
		return true
	}
	if _, ok := listItemContent(trimmed); ok {
		return true
	}
	// Начало HTML-блока любого вида.
	return htmlBlockStart(trimmed) != htmlNone
}

// checkTable проверяет состав таблицы: единственный точный заголовок, следующий
// за ним разделитель из шести ячеек, хотя бы одна строка данных подряд и
// отсутствие оторванных строк.
func checkTable(view []visibleLine) []string {
	texts := make([]string, len(view))
	for index, line := range view {
		if !line.hidden {
			texts[index] = line.text
		}
	}
	var headers []int
	for index, text := range texts {
		if cells, ok := tableRow(text); ok && sameCells(cells, tableHeader) {
			headers = append(headers, index)
		}
	}
	if len(headers) == 0 {
		return []string{"Отсутствует заголовок накопительной таблицы CodeRabbit"}
	}
	if len(headers) > 1 {
		return []string{"Заголовок накопительной таблицы CodeRabbit повторяется: нужна одна таблица"}
	}
	start := headers[0]
	if !delimiterRow(texts, start+1) {
		return []string{fmt.Sprintf(
			"За заголовком накопительной таблицы CodeRabbit нет строки-разделителя из %d столбцов",
			tableColumns)}
	}
	// Канонический заголовок обязан начинать таблицу, а не быть строкой уже
	// открытой чужой таблицы: по GFM такая строка остаётся телом предыдущей, и
	// отчёта из шести столбцов в документе нет.
	if !opensTable(texts, start) {
		return []string{"Заголовок накопительной таблицы CodeRabbit не открывает собственную таблицу"}
	}
	var rows [][]string
	var lines []int
	end := len(texts)
	for index := start + 2; index < len(texts); index++ {
		// Начало другого блока проверяется раньше разбора строки: по GFM
		// заголовок «## Summary | Notes» закрывает таблицу, а не становится её
		// строкой из двух ячеек, хотя черта в нём есть.
		if startsNewBlock(texts[index]) {
			end = index
			break
		}
		cells, ok := tableRow(texts[index])
		if !ok {
			// Непустая строка без «|» — по GFM это строка таблицы с одной
			// ячейкой, а не конец таблицы: иначе «Примечание» сразу после строки
			// данных молча добавлялось бы к отчёту. Таблицу завершает только
			// пустая строка (скрытые строки тоже пусты) или начало другого
			// блока.
			if !blank(texts[index]) {
				cells = []string{strings.TrimSpace(texts[index])}
				ok = true
			}
		}
		if !ok {
			end = index
			break
		}
		rows = append(rows, cells)
		lines = append(lines, index)
	}
	if len(rows) == 0 {
		return []string{"В накопительной таблице CodeRabbit нет ни одной строки данных"}
	}
	problems := checkRows(rows, lines)
	// После разрыва отклоняем вероятные продолжения таблицы: строку с внешней
	// «|», строку из шести ячеек и строку с номером итерации. Обычная проза с
	// одиночным разделителем продолжением таблицы не является.
	for index := end; index < len(texts); index++ {
		// Начало другого блока продолжением таблицы не является: заголовок
		// «## Summary | Scope | … | Owners» из шести «ячеек» — это заголовок,
		// а не оторванная строка отчёта.
		if startsNewBlock(texts[index]) {
			continue
		}
		cells, ok := tableRow(texts[index])
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(texts[index])
		if strings.HasPrefix(trimmed, "|") || len(cells) == tableColumns ||
			iterationPrefix.MatchString(strings.TrimSpace(cells[0])) {
			problems = append(problems, fmt.Sprintf(
				"Строка %d: строка таблицы CodeRabbit оторвана от накопительной таблицы", index+1))
		}
	}
	return problems
}

// opensTable сообщает, что строка открывает собственную таблицу GFM: над ней
// нет строки таблицы, которую она продолжала бы.
func opensTable(texts []string, index int) bool {
	if index == 0 {
		return true
	}
	previous := texts[index-1]
	if blank(previous) || startsNewBlock(previous) {
		return true
	}
	// Разделитель предыдущей таблицы: «|---|» и подобное.
	return delimiterRow(texts, index-1)
}

// delimiterRow проверяет строку-разделитель таблицы: шесть ячеек из дефисов с
// необязательным выравниванием.
func delimiterRow(texts []string, index int) bool {
	if index < 0 || index >= len(texts) {
		return false
	}
	cells, ok := tableRow(texts[index])
	if !ok || len(cells) != tableColumns {
		return false
	}
	for _, cell := range cells {
		if !delimiterCell.MatchString(cell) {
			return false
		}
	}
	return true
}

// checkRows проверяет строки данных: число столбцов, непустоту ячеек, связь с
// итерацией и reviewed HEAD, канонические решения, последовательность номеров и
// семантику clean.
func checkRows(rows [][]string, lines []int) []string {
	var problems []string
	seen := make(map[int64]bool)
	var current int64
	currentSHA := ""
	iterationRows := 0
	cleanIteration := false
	for index, cells := range rows {
		lineNumber := lines[index] + 1
		if len(cells) != tableColumns {
			problems = append(problems, fmt.Sprintf(
				"Строка %d: строка таблицы CodeRabbit содержит %d столбцов вместо %d",
				lineNumber, len(cells), tableColumns))
			continue
		}
		if emptyCell(cells) {
			problems = append(problems, fmt.Sprintf(
				"Строка %d: строка таблицы CodeRabbit содержит пустую ячейку", lineNumber))
			continue
		}
		match := iterationCell.FindStringSubmatch(cells[0])
		if match == nil {
			problems = append(problems, fmt.Sprintf(
				"Строка %d: строка таблицы CodeRabbit не связана с итерацией и reviewed HEAD", lineNumber))
			continue
		}
		number, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"Строка %d: строка таблицы CodeRabbit не связана с итерацией и reviewed HEAD", lineNumber))
			continue
		}
		sha := match[2]
		if number != current {
			if number != current+1 {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: номера итераций должны идти последовательно с 1: получен %d после %d",
					lineNumber, number, current))
			}
			if seen[number] {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: строки итерации %d должны идти подряд", lineNumber, number))
			}
			seen[number] = true
			current = number
			currentSHA = sha
			iterationRows = 0
			cleanIteration = false
		} else if sha != currentSHA {
			problems = append(problems, fmt.Sprintf(
				"Строка %d: строки одной итерации должны иметь один reviewed HEAD", lineNumber))
		}
		iterationRows++
		decision := cells[4]
		switch {
		case !decisionSet[decision]:
			problems = append(problems, fmt.Sprintf(
				"Строка %d: столбец «Решение» содержит неканоническое значение; допустимы: %s",
				lineNumber, valueList(canonicalDecisions)))
		case decision == cleanDecision:
			cleanIteration = true
			if cells[1] != emptyValue || cells[2] != emptyValue {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: clean-строка требует «%s» в столбцах «Уровень» и «Путь»",
					lineNumber, emptyValue))
			}
			if iterationRows > 1 {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: clean не может сочетаться со строками замечаний одной итерации",
					lineNumber))
			}
		default:
			if cells[1] == emptyValue || cells[2] == emptyValue {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: «%s» в уровне или пути допустим только в clean-строке",
					lineNumber, emptyValue))
			} else if !severitySet[cells[1]] {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: столбец «Уровень» содержит не-провайдерское значение; допустимы: %s",
					lineNumber, valueList(canonicalSeverities)))
			}
			if cleanIteration {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: clean не может сочетаться со строками замечаний одной итерации",
					lineNumber))
			}
		}
	}
	return problems
}

func emptyCell(cells []string) bool {
	for _, cell := range cells {
		if visibleCell(cell) == "" {
			return true
		}
	}
	return false
}

// visibleCell возвращает отображаемое содержимое ячейки: границы ячеек уже
// определены по исходной строке, поэтому HTML-комментарий может только скрывать
// содержимое, но не создавать ячейку и не делать её непустой.
func visibleCell(cell string) string {
	inside := false
	return strings.TrimSpace(stripComments(cell, &inside))
}

// apiClient выполняет только GET к api.github.com. Транспорт и задержки
// подменяются тестами; защита от перенаправления и пределы остаются в клиенте.
type apiClient struct {
	token string
	http  *http.Client
	sleep func(time.Duration)
}

func newAPIClient(token string, transport http.RoundTripper) *apiClient {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &apiClient{
		token: token,
		http: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			// Ни одно перенаправление не выполняется: токен не уходит на другой адрес.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		sleep: time.Sleep,
	}
}

// apiError отличает повторяемый сбой от окончательного отказа. Текст ошибки —
// собственная константа: URL, тело ответа и токен не попадают в диагностику.
type apiError struct {
	message   string
	retryable bool
}

func (e *apiError) Error() string { return e.message }

type apiResponse struct {
	body []byte
	link string
}

// get выполняет GET с ограниченным числом повторов только для временных
// транспортных сбоев и 5xx.
func (c *apiClient) get(path string) (apiResponse, error) {
	for attempt := range maxAttempts {
		if attempt > 0 {
			c.sleep(retryUnit * time.Duration(attempt))
		}
		response, err := c.once(apiBase + path)
		if err == nil {
			return response, nil
		}
		var failure *apiError
		if !errors.As(err, &failure) || !failure.retryable {
			return apiResponse{}, err
		}
	}
	return apiResponse{}, errors.New("не удалось получить ответ GitHub API после повторов")
}

// once выполняет одну попытку запроса и всегда закрывает тело ответа.
func (c *apiClient) once(address string) (apiResponse, error) {
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return apiResponse{}, errors.New("не удалось построить запрос к GitHub API")
	}
	request.Header.Set("Accept", apiAccept)
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	request.Header.Set("User-Agent", apiUserAgent)
	response, err := c.http.Do(request)
	if err != nil {
		// Текст транспортной ошибки может содержать адрес: не выводим его.
		return apiResponse{}, &apiError{message: "сбой транспорта при обращении к GitHub API", retryable: true}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 500 && response.StatusCode <= 599 {
		return apiResponse{}, &apiError{message: "GitHub API вернул временную ошибку сервера", retryable: true}
	}
	if response.StatusCode != http.StatusOK {
		return apiResponse{}, &apiError{message: fmt.Sprintf(
			"GitHub API вернул HTTP-статус %d", response.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return apiResponse{}, &apiError{message: "не удалось прочитать ответ GitHub API", retryable: true}
	}
	if len(data) > maxResponseSize {
		return apiResponse{}, &apiError{message: "ответ GitHub API превышает допустимый размер"}
	}
	if !utf8.Valid(data) {
		return apiResponse{}, &apiError{message: "ответ GitHub API содержит некорректный UTF-8"}
	}
	return apiResponse{body: data, link: response.Header.Get("Link")}, nil
}

// pullSnapshot — поля PR, которые нужно сверить с событием и между двумя
// чтениями; структура сравнима целиком.
type pullSnapshot struct {
	Number      int64
	AuthorID    int64
	AuthorLogin string
	HeadSHA     string
	HeadRef     string
	BaseSHA     string
	BaseRef     string
	// BaseRepoID — числовой идентификатор репозитория назначения. Ноль —
	// метаданные его не содержат, и тогда каноническая ссылка пагинации
	// отвергается: принадлежность подтвердить нечем.
	BaseRepoID  int64
	Body        string
	BodyPresent bool
}

// issueComment — запись комментариев PR; отсутствие автора допускается только у
// немаркированного сообщения.
type issueComment struct {
	ID      int64
	Body    string
	UserID  int64
	HasUser bool
}

// checkPublished выполняет сверку опубликованного состояния только на чтение: PR,
// полная выдача комментариев и повторное чтение PR. Третий результат сообщает,
// что служебный комментарий найден и проверен.
func checkPublished(getenv func(string) string, client *apiClient, event eventSnapshot) ([]string, bool, error) {
	repository := getenv("GITHUB_REPOSITORY")
	token := getenv("GITHUB_TOKEN")
	if !validRepository(repository) {
		return nil, false, errors.New("отсутствует или некорректен GITHUB_REPOSITORY")
	}
	if !validToken(token) {
		return nil, false, errors.New("отсутствует или некорректен GITHUB_TOKEN")
	}
	if client == nil {
		client = newAPIClient(token, nil)
	}
	if event.Repository != repository {
		return nil, false, errors.New("репозиторий события не совпадает с GITHUB_REPOSITORY")
	}
	pullPath := fmt.Sprintf("/repos/%s/pulls/%d", repository, event.Number)
	first, err := fetchPull(client, pullPath)
	if err != nil {
		return nil, false, err
	}
	if first.Number != event.Number {
		return nil, false, errors.New("номер PR из GitHub API не совпадает с событием")
	}
	if first.HeadSHA != event.HeadSHA {
		return nil, false, errors.New("HEAD PR изменился после события: нужна новая проверка")
	}
	comments, err := client.listComments(repository, event.Number, first.BaseRepoID)
	if err != nil {
		return nil, false, err
	}
	problems, found := validateComments(comments, first.AuthorID)
	// PR мог измениться во время постраничного чтения: не подтверждать
	// смешанный снимок метаданных и тела.
	second, err := fetchPull(client, pullPath)
	if err != nil {
		return nil, false, err
	}
	if second != first {
		return nil, false, errors.New("PR изменился во время проверки: нужна повторная проверка")
	}
	return problems, found, nil
}

// fetchPull читает объект PR и проверяет обязательные поля.
func fetchPull(client *apiClient, path string) (pullSnapshot, error) {
	response, err := client.get(path)
	if err != nil {
		return pullSnapshot{}, err
	}
	return parsePull(response.body)
}

// parsePull разбирает объект PR: номер, фактический автор, head/base и тело.
func parsePull(data []byte) (pullSnapshot, error) {
	var snapshot pullSnapshot
	object, err := jsonObject(data)
	if err != nil {
		return snapshot, errors.New("некорректный объект PR из GitHub API")
	}
	if snapshot.Number, err = positiveID(object["number"]); err != nil {
		return snapshot, errors.New("некорректный номер PR из GitHub API")
	}
	user, err := jsonObjectField(object, "user")
	if err != nil {
		return snapshot, errors.New("некорректный автор PR из GitHub API")
	}
	if snapshot.AuthorID, err = positiveID(user["id"]); err != nil {
		return snapshot, errors.New("некорректный числовой идентификатор автора PR из GitHub API")
	}
	if snapshot.AuthorLogin, err = nonEmptyString(user, "login"); err != nil {
		return snapshot, errors.New("некорректный логин автора PR из GitHub API")
	}
	head, err := jsonObjectField(object, "head")
	if err != nil {
		return snapshot, errors.New("некорректный head PR из GitHub API")
	}
	if snapshot.HeadSHA, err = fullSHAField(head, "sha"); err != nil {
		return snapshot, errors.New("некорректный head SHA PR из GitHub API")
	}
	if snapshot.HeadRef, err = nonEmptyString(head, "ref"); err != nil {
		return snapshot, errors.New("некорректная ветка head PR из GitHub API")
	}
	base, err := jsonObjectField(object, "base")
	if err != nil {
		return snapshot, errors.New("некорректный base PR из GitHub API")
	}
	// base.repo — репозиторий назначения. Его идентификатор подтверждает
	// принадлежность канонической ссылки пагинации; отсутствие поля не ошибка,
	// но каноническая ссылка тогда не принимается.
	if repo, repoErr := jsonObjectField(base, "repo"); repoErr == nil {
		if id, idErr := positiveID(repo["id"]); idErr == nil {
			snapshot.BaseRepoID = id
		}
	}
	if snapshot.BaseSHA, err = fullSHAField(base, "sha"); err != nil {
		return snapshot, errors.New("некорректный base SHA PR из GitHub API")
	}
	if snapshot.BaseRef, err = nonEmptyString(base, "ref"); err != nil {
		return snapshot, errors.New("некорректная ветка base PR из GitHub API")
	}
	if snapshot.Body, snapshot.BodyPresent, err = nullableString(object, "body"); err != nil {
		return snapshot, errors.New("некорректное тело PR из GitHub API")
	}
	return snapshot, nil
}

// listComments читает полную постраничную выдачу комментариев, проверяя записи,
// повторы ID и заголовок Link. repositoryID — числовой идентификатор
// репозитория назначения: он нужен для сверки канонической ссылки пагинации.
func (c *apiClient) listComments(repository string, number, repositoryID int64) ([]issueComment, error) {
	comments, pages, err := c.readComments(repository, number, repositoryID)
	if err != nil {
		return nil, err
	}
	if pages < 2 {
		// Выдача поместилась в одну страницу: сдвига между страницами быть не
		// может, повторное чтение ничего не докажет.
		return comments, nil
	}
	// Постраничное чтение не является снимком: удаление комментария между
	// страницами сдвигает выдачу, и запись может быть пропущена без повтора ID.
	// Поэтому многостраничная выдача перечитывается целиком и сверяется по
	// набору ID; расхождение — отказ, а не молчаливый пропуск.
	again, _, err := c.readComments(repository, number, repositoryID)
	if err != nil {
		return nil, err
	}
	if !sameCommentIDs(comments, again) {
		return nil, errors.New("выдача комментариев изменилась во время постраничного чтения")
	}
	return comments, nil
}

// sameCommentIDs сравнивает две выдачи по набору идентификаторов.
func sameCommentIDs(first, second []issueComment) bool {
	if len(first) != len(second) {
		return false
	}
	firstIDs := make(map[int64]bool, len(first))
	for _, comment := range first {
		firstIDs[comment.ID] = true
	}
	for _, comment := range second {
		if !firstIDs[comment.ID] {
			return false
		}
	}
	return true
}

// readComments читает выдачу комментариев постранично и возвращает записи
// вместе с числом прочитанных страниц.
func (c *apiClient) readComments(repository string, number, repositoryID int64) ([]issueComment, int, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repository, number)
	page := 1
	pages := 0
	seen := make(map[int64]bool)
	var comments []issueComment
	for {
		if page > maxPages {
			return nil, pages, errors.New("превышен предел страниц комментариев GitHub API")
		}
		response, err := c.get(fmt.Sprintf("%s?per_page=%d&page=%d", path, pageSize, page))
		if err != nil {
			return nil, pages, err
		}
		entries, err := jsonArray(response.body)
		if err != nil || len(entries) > pageSize {
			return nil, pages, errors.New("некорректная страница комментариев GitHub API")
		}
		pages++
		for _, entry := range entries {
			comment, err := parseComment(entry)
			if err != nil {
				return nil, pages, err
			}
			if seen[comment.ID] {
				return nil, pages, errors.New("повторный ID комментария при пагинации")
			}
			seen[comment.ID] = true
			comments = append(comments, comment)
		}
		next, err := nextPage(response.link, path, number, repositoryID, page)
		if err != nil {
			return nil, pages, err
		}
		switch {
		case next > 0:
			if len(entries) == 0 {
				return nil, pages, errors.New("пустая страница содержит ссылку продолжения")
			}
			page = next
		case len(entries) < pageSize:
			// Короткая страница без продолжения — конец выдачи.
			return comments, pages, nil
		default:
			// Ровно сто записей без Link могут быть не последней страницей:
			// запрашиваем следующую, пока не придёт пустая.
			page++
		}
	}
}

// parseComment разбирает запись комментария: положительный числовой идентификатор,
// строковое тело и необязательного автора.
func parseComment(data []byte) (issueComment, error) {
	var comment issueComment
	object, err := jsonObject(data)
	if err != nil {
		return comment, errors.New("некорректная запись комментария GitHub API")
	}
	if comment.ID, err = positiveID(object["id"]); err != nil {
		return comment, errors.New("некорректный ID комментария GitHub API")
	}
	// Тело комментария обязано быть строкой: null и отсутствие поля — отказ.
	body, present, bodyErr := nullableString(object, "body")
	if bodyErr != nil || !present {
		return comment, errors.New("некорректное тело комментария GitHub API")
	}
	comment.Body = body
	user, err := jsonObject(object["user"])
	if err == nil {
		if id, idErr := positiveID(user["id"]); idErr == nil {
			comment.UserID, comment.HasUser = id, true
		}
	}
	return comment, nil
}

// validateComments определяет кандидатов по тексту маркера, включая скрытый и
// неправильно размещённый, и проверяет авторство по числовому идентификатору.
func validateComments(comments []issueComment, authorID int64) ([]string, bool) {
	var marked []issueComment
	for _, comment := range comments {
		if strings.Contains(comment.Body, commentMarker) {
			marked = append(marked, comment)
		}
	}
	if len(marked) > 1 {
		return []string{"Найдено несколько служебных комментариев CodeRabbit"}, false
	}
	if len(marked) == 0 {
		return nil, false
	}
	comment := marked[0]
	// Узкое исключение ботов не действует: маркированный комментарий обязан
	// принадлежать фактическому автору PR.
	if !comment.HasUser || comment.UserID != authorID {
		return []string{"Автор служебного комментария CodeRabbit не является автором PR"}, false
	}
	return validateComment(comment.Body), true
}

// nextPage проверяет заголовок пагинации и возвращает номер следующей страницы.
// Ноль — продолжения нет. Следующий запрос строится из доверенного path, а не
// из адреса ответа. repositoryID — идентификатор репозитория назначения:
// каноническая ссылка принимается только при его точном совпадении.
func nextPage(link, path string, number, repositoryID int64, page int) (int, error) {
	if strings.TrimSpace(link) == "" {
		return 0, nil
	}
	relations := make(map[string]string)
	for item := range strings.SplitSeq(link, ",") {
		match := linkItem.FindStringSubmatch(item)
		if match == nil {
			return 0, errors.New("некорректный заголовок пагинации GitHub API")
		}
		if _, exists := relations[match[2]]; exists {
			return 0, errors.New("некорректный заголовок пагинации GitHub API")
		}
		relations[match[2]] = match[1]
	}
	target, ok := relations["next"]
	if !ok {
		return 0, nil
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return 0, errors.New("недопустимая ссылка следующей страницы")
	}
	if parsed.Scheme != "https" || parsed.Host != apiHost || parsed.User != nil || parsed.Fragment != "" {
		return 0, errors.New("недопустимая ссылка следующей страницы")
	}
	if parsed.Path != path && !canonicalPath(parsed.Path, number, repositoryID) {
		return 0, errors.New("недопустимая ссылка следующей страницы")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 2 {
		return 0, errors.New("некорректная ссылка следующей страницы")
	}
	if len(query["page"]) != 1 || query["page"][0] != strconv.Itoa(page+1) {
		return 0, errors.New("некорректная ссылка следующей страницы")
	}
	if len(query["per_page"]) != 1 || query["per_page"][0] != strconv.Itoa(pageSize) {
		return 0, errors.New("некорректная ссылка следующей страницы")
	}
	return page + 1, nil
}

// canonicalPath принимает каноническую ссылку /repositories/{id}/… для того же
// PR: GitHub иногда возвращает её вместо /repos/{owner}/{repo}/…. Идентификатор
// репозитория сверяется по значению, а не только по форме: ссылка на чужой
// ресурс не подтверждает полноту чтения. Если достоверного идентификатора нет,
// каноническая ссылка отвергается.
func canonicalPath(path string, number, repositoryID int64) bool {
	if repositoryID <= 0 {
		return false
	}
	prefix := "/repositories/"
	suffix := "/issues/" + strconv.FormatInt(number, 10) + "/comments"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.ContainsAny(id, "/") {
		return false
	}
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil || value != repositoryID {
		return false
	}
	return true
}

// eventSnapshot — минимальный проверенный снимок события pull_request.
type eventSnapshot struct {
	Repository string
	Number     int64
	HeadSHA    string
}

// readEvent читает снимок события и проверяет типы и обязательные поля.
func readEvent(path string) (eventSnapshot, error) {
	var snapshot eventSnapshot
	if path == "" {
		return snapshot, errors.New("не указан файл события")
	}
	text, err := readTextFile(path)
	if err != nil {
		return snapshot, err
	}
	payload, err := jsonObject([]byte(text))
	if err != nil {
		return snapshot, errors.New("файл события содержит некорректный JSON")
	}
	repository, err := jsonObjectField(payload, "repository")
	if err != nil {
		return snapshot, errors.New("в событии нет объекта repository")
	}
	if snapshot.Repository, err = nonEmptyString(repository, "full_name"); err != nil {
		return snapshot, errors.New("в событии нет repository.full_name")
	}
	pull, err := jsonObjectField(payload, "pull_request")
	if err != nil {
		return snapshot, errors.New("в событии нет объекта pull_request")
	}
	if snapshot.Number, err = positiveID(pull["number"]); err != nil {
		return snapshot, errors.New("в событии нет положительного номера PR")
	}
	head, err := jsonObjectField(pull, "head")
	if err != nil {
		return snapshot, errors.New("в событии нет объекта pull_request.head")
	}
	if snapshot.HeadSHA, err = fullSHAField(head, "sha"); err != nil {
		return snapshot, errors.New("в событии нет полного head SHA")
	}
	return snapshot, nil
}

func jsonObject(data []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("не объект JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, errors.New("не объект JSON")
	}
	return object, nil
}

func jsonArray(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("не массив JSON")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, errors.New("не массив JSON")
	}
	return entries, nil
}

func jsonObjectField(object map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, exists := object[key]
	if !exists {
		return nil, errors.New("нет поля")
	}
	return jsonObject(raw)
}

func nonEmptyString(object map[string]json.RawMessage, key string) (string, error) {
	raw, exists := object[key]
	if !exists {
		return "", errors.New("нет поля")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", errors.New("не строка")
	}
	return value, nil
}

func fullSHAField(object map[string]json.RawMessage, key string) (string, error) {
	value, err := nonEmptyString(object, key)
	if err != nil || !hexSHA.MatchString(value) {
		return "", errors.New("не полный SHA")
	}
	return value, nil
}

// nullableString различает строку, null и отсутствующее поле.
func nullableString(object map[string]json.RawMessage, key string) (string, bool, error) {
	raw, exists := object[key]
	if !exists {
		return "", false, errors.New("нет поля")
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, errors.New("не строка и не null")
	}
	return value, true, nil
}

// positiveID принимает только положительное целое JSON-число: строка, true,
// дробь и ноль отвергаются.
func positiveID(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("нет поля")
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return 0, errors.New("не положительное целое")
	}
	return value, nil
}

// validRepository проверяет вид owner/name без обхода каталогов.
func validRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !repositoryID.MatchString(part) {
			return false
		}
	}
	return true
}

// validToken требует непустой токен из печатных ASCII-символов.
func validToken(token string) bool {
	if token == "" {
		return false
	}
	for _, char := range token {
		if char < 33 || char > 126 {
			return false
		}
	}
	return true
}
