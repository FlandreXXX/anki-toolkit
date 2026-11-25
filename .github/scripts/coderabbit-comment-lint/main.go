// Программа проверяет накопительный комментарий CodeRabbit: единственный
// служебный маркер и одну связную шестиколоночную таблицу решений в видимом
// Markdown, а в сетевом режиме — ещё и опубликованное состояние PR по GitHub API.
//
// Проверяется форма, а не правдивость содержания: линтер не доказывает, что
// ревью действительно было, что findings приведены полно и что коммиты
// существуют.
//
// # Документированное подмножество Markdown
//
// Видимость маркера и таблицы определяется только следующими конструкциями;
// всё, что выходит за этот список, считается сомнительной видимостью и даёт
// диагностируемый отказ, а не догадку:
//
//   - fenced-блоки ``` и ~~~ (три и более символов) скрывают текст до
//     согласованного закрытия: тот же символ и не короче открывающего, без
//     строки информации. Блок, открытый в строке пункта списка или на
//     последующей строке с отступом от начала его содержимого, скрывает текст
//     до закрытия или до выхода из пункта; незакрытое и несовпадающее
//     ограждение скрывает остаток документа;
//   - код с отступом от четырёх пробелов, отсчитанных от начала содержимого
//     текущего пункта списка, скрыт;
//   - HTML-комментарии <!-- … -->, включая незакрытые, скрывают текст; внутри
//     code span и внутри fenced-блока литеральный «<!--» комментарий не
//     открывает;
//   - блок-цитата «>» скрыта;
//   - raw HTML: <script>, <pre>, <style>, <textarea> скрывают текст до
//     закрывающего тега, прочие блочные теги (<div> и подобные) — до пустой
//     строки.
//
// Строка, которая началась внутри скрытого блока, считается скрытой целиком,
// даже если блок закрывается на ней же: неоднозначный случай решается отказом.
//
// Полный CommonMark не реализуется. Вне подмножества остаются: определения
// ссылок и сноски, заголовки setext, таблицы без строки-разделителя,
// многоуровневые и перемежающиеся списки, блоки кода внутри цитаты, inline-HTML
// и HTML-сущности внутри ячеек, а также любые конструкции, где видимость зависит
// от вложенности контейнеров глубже одноуровневого пункта списка. Если такая
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
	"slices"
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

	// emptyValue заполняет «Уровень» и «Путь» в clean-строке и допустим только
	// там.
	emptyValue      = "—"
	cleanDecision   = "clean"
	decisionList    = "confirmed, partially confirmed, false positive, stale, duplicate, out of scope, unconfirmed, clean"
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

// canonicalDecisions — канонические решения; шкала severity провайдера здесь не
// фиксируется, поэтому «Уровень» принимает любое непустое значение.
var canonicalDecisions = map[string]bool{
	"confirmed":           true,
	"partially confirmed": true,
	"false positive":      true,
	"stale":               true,
	"duplicate":           true,
	"out of scope":        true,
	"unconfirmed":         true,
	cleanDecision:         true,
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
	htmlTagStart = regexp.MustCompile(`^ {0,3}<(/?)([A-Za-z][A-Za-z0-9-]*)(\s|/?>)`)
)

// rawTextTags скрывают текст до своего закрывающего тега, а не до пустой строки.
var rawTextTags = map[string]bool{"script": true, "pre": true, "style": true, "textarea": true}

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
	switch {
	case *commentFile != "" && *eventFile != "":
		return usageError(stderr, "флаги --comment-file и --event-file взаимоисключающие")
	case *commentFile == "" && *eventFile == "":
		return usageError(stderr, "нужен ровно один режим: --comment-file или --event-file")
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
		fmt.Fprintln(stdout, "Служебный комментарий CodeRabbit не найден: Conversation прочитана полностью")
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
// комментария или события не должны формировать управляющие строки workflow.
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
		return []string{"Маркер " + commentMarker + " скрыт внутри комментария, кода, цитаты или raw HTML"}
	}
	if strings.TrimSpace(lines[index]) != commentMarker {
		return []string{"Маркер " + commentMarker + " должен занимать отдельную строку"}
	}
	return nil
}

// visibleLine — результат разбора одной строки: видимый текст без
// HTML-комментариев либо признак, что строка скрыта.
type visibleLine struct {
	text   string
	hidden bool
}

// fence описывает активный fenced-блок.
type fence struct {
	ch  byte // маркер ограждения: '`' или '~'
	n   int  // длина маркера открытия
	col int  // 0 — блок верхнего уровня; >0 — блок внутри пункта списка
}

// viewer отслеживает состояние видимости по строкам документа.
type viewer struct {
	fence     *fence
	comment   bool
	rawTag    string
	htmlBlock bool
	listCol   int
}

// visibleLines размечает строки документа: скрытые блоки заменяются признаком
// hidden, остальные получают видимый текст. Строка, начавшаяся внутри скрытого
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
	if v.rawTag != "" {
		if closesRawTag(line, v.rawTag) {
			v.rawTag = ""
		}
		return visibleLine{hidden: true}
	}
	if v.htmlBlock {
		if blank(line) {
			v.htmlBlock = false
		}
		return visibleLine{hidden: true}
	}
	if v.comment {
		if strings.Contains(line, "-->") {
			v.comment = false
		}
		return visibleLine{hidden: true}
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
	if name, raw, ok := htmlBlockStart(line); ok {
		if raw {
			v.rawTag = name
		} else {
			v.htmlBlock = true
		}
		return visibleLine{hidden: true}
	}
	// Строка, начинающаяся с HTML-комментария, по GFM образует HTML-блок
	// (тип 2), который заканчивается на строке с «-->». Текст такой строки —
	// уже не разметка таблицы, поэтому заголовок из неё не извлекается.
	if strings.HasPrefix(strings.TrimLeft(line, " "), htmlCommentStart) {
		rest := stripComments(line, &v.comment)
		switch {
		case v.comment:
			// Комментарий не закрылся: скрыт остаток строки и следующие строки.
			return visibleLine{hidden: true}
		case strings.TrimSpace(rest) == "":
			// Комментарий занимает строку целиком: это допустимое место
			// служебного маркера, но не строка таблицы.
			return visibleLine{text: ""}
		default:
			// После закрытия комментария остаток строки остаётся HTML-текстом.
			return visibleLine{hidden: true}
		}
	}
	text := stripComments(line, &v.comment)
	return visibleLine{text: text}
}

// htmlBlockStart распознаёт начало блока raw HTML. Второй результат — тег из
// rawTextTags, который скрывает текст до своего закрытия, третий — что строка
// вообще открывает HTML-блок.
func htmlBlockStart(line string) (string, bool, bool) {
	match := htmlTagStart.FindStringSubmatch(line)
	if match == nil {
		return "", false, false
	}
	name := strings.ToLower(match[2])
	if rawTextTags[name] && match[1] == "" {
		if !closesRawTag(line, name) {
			return name, true, true
		}
	}
	return name, false, true
}

// closesRawTag сообщает, что в строке есть закрывающий тег raw-блока. Тег
// сверяется целиком: «</prefix>» не закрывает «<pre>», иначе текст после него
// ошибочно считался бы видимым.
func closesRawTag(line, name string) bool {
	lower := strings.ToLower(line)
	closing := "</" + name
	for from := 0; ; {
		index := strings.Index(lower[from:], closing)
		if index < 0 {
			return false
		}
		end := from + index + len(closing)
		// Имя тега заканчивается пробелом, «>» или «/»: иначе это другой тег.
		if end < len(lower) {
			switch lower[end] {
			case ' ', '\t', '>', '/', '\r', '\n':
				return true
			}
		}
		from = end
	}
}

// stripComments убирает HTML-комментарии, включая многострочные и незакрытые:
// незакрытый комментарий скрывает остаток строки и следующие строки. Строчный
// код в обратных кавычках — литеральный контекст: «<!--» внутри него
// комментарий не открывает. Вызывается вне fenced-блоков и блоков кода с
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

// splitUnescapedPipes делит строку только по неэкранированной «|» с учётом
// чётности обратных слэшей: «\|» — литеральная черта.
func splitUnescapedPipes(text string) []string {
	var parts []string
	start := 0
	slashes := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '\\':
			slashes++
			continue
		case '|':
			if slashes%2 == 0 {
				parts = append(parts, text[start:index])
				start = index + 1
			}
		}
		slashes = 0
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
	// Начало raw-HTML-блока или одиночный HTML-тег.
	if _, _, ok := htmlBlockStart(trimmed); ok {
		return true
	}
	return strings.HasPrefix(trimmed, htmlCommentStart)
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
	var rows [][]string
	var lines []int
	end := len(texts)
	for index := start + 2; index < len(texts); index++ {
		cells, ok := tableRow(texts[index])
		if !ok {
			// Непустая строка без «|» — по GFM это строка таблицы с одной
			// ячейкой, а не конец таблицы: иначе «Примечание» сразу после строки
			// данных молча добавлялось бы к отчёту. Таблицу завершает только
			// пустая строка (скрытые строки тоже пусты) или начало другого
			// блока.
			if !blank(texts[index]) && !startsNewBlock(texts[index]) {
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
		case !canonicalDecisions[decision]:
			problems = append(problems, fmt.Sprintf(
				"Строка %d: столбец «Решение» содержит неканоническое значение; допустимы: %s",
				lineNumber, decisionList))
		case decision == cleanDecision:
			cleanIteration = true
			if cells[1] != emptyValue || cells[2] != emptyValue {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: clean-строка требует «%s» в столбцах «Уровень» и «Путь»",
					lineNumber, emptyValue))
			}
			if iterationRows > 1 {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: clean не может сочетаться со строками findings одной итерации",
					lineNumber))
			}
		default:
			if cells[1] == emptyValue || cells[2] == emptyValue {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: «%s» в уровне или пути допустим только в clean-строке",
					lineNumber, emptyValue))
			}
			if cleanIteration {
				problems = append(problems, fmt.Sprintf(
					"Строка %d: clean не может сочетаться со строками findings одной итерации",
					lineNumber))
			}
		}
	}
	return problems
}

func emptyCell(cells []string) bool {
	return slices.Contains(cells, "")
}

// apiClient выполняет только GET к api.github.com. Транспорт и задержки
// подменяются тестами; защита от redirect и пределы остаются в клиенте.
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
			// Ни один redirect не выполняется: токен не уходит на другой адрес.
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
	Body        string
	BodyPresent bool
}

// issueComment — запись Conversation; отсутствие автора допускается только у
// немаркированного сообщения.
type issueComment struct {
	ID      int64
	Body    string
	UserID  int64
	HasUser bool
}

// checkPublished выполняет read-only сверку опубликованного состояния: PR,
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
	comments, err := client.listComments(repository, event.Number)
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
		return snapshot, errors.New("некорректный numeric ID автора PR из GitHub API")
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
// повторы ID и заголовок Link.
func (c *apiClient) listComments(repository string, number int64) ([]issueComment, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repository, number)
	page := 1
	seen := make(map[int64]bool)
	var comments []issueComment
	for {
		if page > maxPages {
			return nil, errors.New("превышен предел страниц комментариев GitHub API")
		}
		response, err := c.get(fmt.Sprintf("%s?per_page=%d&page=%d", path, pageSize, page))
		if err != nil {
			return nil, err
		}
		entries, err := jsonArray(response.body)
		if err != nil || len(entries) > pageSize {
			return nil, errors.New("некорректная страница комментариев GitHub API")
		}
		for _, entry := range entries {
			comment, err := parseComment(entry)
			if err != nil {
				return nil, err
			}
			if seen[comment.ID] {
				return nil, errors.New("повторный ID комментария при пагинации")
			}
			seen[comment.ID] = true
			comments = append(comments, comment)
		}
		next, err := nextPage(response.link, path, number, page)
		if err != nil {
			return nil, err
		}
		switch {
		case next > 0:
			if len(entries) == 0 {
				return nil, errors.New("пустая страница содержит ссылку продолжения")
			}
			page = next
		case len(entries) < pageSize:
			// Короткая страница без продолжения — конец выдачи.
			return comments, nil
		default:
			// Ровно сто записей без Link могут быть не последней страницей:
			// запрашиваем следующую, пока не придёт пустая.
			page++
		}
	}
}

// parseComment разбирает запись Conversation: положительный numeric ID,
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
// неправильно размещённый, и проверяет авторство по numeric ID.
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
// из адреса ответа.
func nextPage(link, path string, number int64, page int) (int, error) {
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
	if parsed.Path != path && !canonicalPath(parsed.Path, number) {
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
// PR: GitHub иногда возвращает её вместо /repos/{owner}/{repo}/….
func canonicalPath(path string, number int64) bool {
	prefix := "/repositories/"
	suffix := "/issues/" + strconv.FormatInt(number, 10) + "/comments"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.ContainsAny(id, "/") {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	return id[0] != '0'
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
