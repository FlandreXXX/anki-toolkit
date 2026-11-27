// Программа проверяет структуру описания PR: пять обязательных разделов в
// установленном относительном порядке, содержательный видимый текст каждого
// раздела, общий предел 10 000 кодовых точек Unicode и отсутствие жёстких
// переносов строки внутри абзацев.
//
// Проверяется структура, а не правдивость содержания: никаких HTTP-запросов,
// токенов, анализа CI-статусов и бенчмарков здесь нет.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxBodyPoints = 10_000
	usageLine     = "Использование: pr-body-lint (--body-file <путь> | --event-file <путь>)"

	// wrapHint — общая часть диагностики: формулировка правила, а не адрес.
	wrapHint = "абзац пишется одной строкой, отдельные абзацы разделяются пустой строкой"
)

// requiredSections задаёт обязательные разделы в требуемом относительном порядке.
var requiredSections = []string{"Цель", "Изменения", "Проверки", "CI", "Ограничения"}

// botPrefixes сопоставляет аккаунт бота префиксу его ветки: исключение
// предоставляется только паре «свой бот — свой префикс».
var botPrefixes = map[string]string{
	"renovate[bot]":   "renovate/",
	"dependabot[bot]": "dependabot/",
}

// placeholders — очевидные заглушки, которые не считаются содержанием раздела.
var placeholders = map[string]bool{
	"": true, "-": true, "—": true, "–": true, "?": true, "…": true, "...": true,
	"n/a": true, "na": true, "none": true, "нет": true, "не применимо": true,
	"todo": true, "tbd": true, "заполнить": true, "уточнить": true,
	"см. ниже": true, "см. выше": true,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run разбирает аргументы и вход, затем печатает диагностику.
// Коды выхода: 0 — контракт соблюдён (или сработало исключение ботов),
// 1 — нарушение контракта, 2 — ошибка использования или входа.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pr-body-lint", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	bodyFile := flags.String("body-file", "", "локальный Markdown-файл с описанием PR")
	eventFile := flags.String("event-file", "", "JSON-снимок события GitHub pull_request")
	if err := flags.Parse(args); err != nil {
		// Сообщение пакета flag английское: заменяем его своим.
		return usageError(stderr, "неизвестный флаг или отсутствует его значение")
	}
	if flags.NArg() > 0 {
		return usageError(stderr, "лишние позиционные аргументы")
	}
	given := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { given[item.Name] = true })
	switch {
	case given["body-file"] && given["event-file"]:
		return usageError(stderr, "флаги --body-file и --event-file взаимоисключающие")
	case *bodyFile == "" && *eventFile == "":
		return usageError(stderr, "нужен ровно один режим: --body-file или --event-file")
	}
	if *bodyFile != "" {
		body, err := readTextFile(*bodyFile)
		if err != nil {
			return inputError(stderr, err)
		}
		return report(validate(body), stdout, stderr)
	}
	body, exempt, err := eventBody(*eventFile)
	if err != nil {
		return inputError(stderr, err)
	}
	if exempt {
		fmt.Fprintln(stdout, "PR бота обновления зависимостей: формат ручных PR не применяется")
		return 0
	}
	return report(validate(body), stdout, stderr)
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "Ошибка использования: %s\n", message)
	fmt.Fprintln(stderr, usageLine)
	return 2
}

func inputError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "Ошибка входа: %v\n", err)
	return 2
}

func report(problems []string, stdout, stderr io.Writer) int {
	if len(problems) == 0 {
		fmt.Fprintln(stdout, "Структура описания PR соответствует контракту")
		return 0
	}
	for _, problem := range problems {
		fmt.Fprintln(stderr, problem)
	}
	return 1
}

// readTextFile читает файл и отвергает некорректный UTF-8.
// Собственное сообщение отделено от технической причины: исходную ошибку ОС
// или стандартной библиотеки Go нельзя терять и нельзя выдавать за перевод.
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

// eventBody извлекает pull_request.body из снимка события. Второй результат
// означает, что сработало узкое исключение бота обновления зависимостей.
func eventBody(path string) (string, bool, error) {
	text, err := readTextFile(path)
	if err != nil {
		return "", false, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return "", false, fmt.Errorf(
			"файл события %s содержит некорректный JSON; техническая причина: %v", path, err)
	}
	pr, ok := jsonObject(payload["pull_request"])
	if !ok {
		if _, exists := payload["pull_request"]; !exists || isJSONNull(payload["pull_request"]) {
			return "", false, errors.New("в событии нет объекта pull_request")
		}
		return "", false, errors.New("поле pull_request не является объектом")
	}
	if automationPR(pr) {
		return "", true, nil
	}
	raw, exists := pr["body"]
	if !exists || isJSONNull(raw) {
		return "", false, nil
	}
	var body string
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", false, errors.New("поле pull_request.body не является строкой")
	}
	return body, false, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// jsonObject возвращает объект JSON; отсутствие, null и не-объект — false.
func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

// jsonString возвращает строковое поле; не строка или отсутствие — "".
func jsonString(object map[string]json.RawMessage, key string) string {
	raw, exists := object[key]
	if !exists {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// automationPR проверяет все три условия исключения одновременно: ровно
// известный аккаунт бота, тип аккаунта Bot и ветка из префикса этого бота.
// Заголовок, метки и sender не читаются вовсе, поэтому подделать исключение
// ими нельзя.
func automationPR(pr map[string]json.RawMessage) bool {
	user, ok := jsonObject(pr["user"])
	if !ok {
		return false
	}
	if jsonString(user, "type") != "Bot" {
		return false
	}
	prefix, ok := botPrefixes[jsonString(user, "login")]
	if !ok {
		return false
	}
	head, ok := jsonObject(pr["head"])
	if !ok {
		return false
	}
	return strings.HasPrefix(jsonString(head, "ref"), prefix)
}

// validate возвращает детерминированный список нарушений контракта;
// пустой список означает, что структура соответствует контракту.
//
// Переводы строк нормализуются здесь — в начале фактической цепочки проверки,
// поэтому и подсчёт кодовых точек, и разбор разделов видят один и тот же текст.
func validate(body string) []string {
	body = normalizeNewlines(body)
	if count := utf8.RuneCountInString(body); count > maxBodyPoints {
		return []string{fmt.Sprintf("Описание PR: %d кодовых точек, максимум %d", count, maxBodyPoints)}
	}
	if strings.TrimSpace(body) == "" {
		return []string{"Описание PR пустое: нужны обязательные разделы " + headingList(requiredSections)}
	}
	parsed := parseSections(body)
	names := make([]string, len(parsed.sections))
	for i, section := range parsed.sections {
		names[i] = section.name
	}
	var problems []string
	for _, required := range requiredSections {
		switch count := countOf(names, required); {
		case count == 0:
			problems = append(problems, "Отсутствует обязательный заголовок: ## "+required)
		case count > 1:
			problems = append(problems, "Заголовок повторяется: ## "+required)
		}
	}
	if len(problems) == 0 {
		problems = append(problems, orderProblems(names)...)
	}
	for _, section := range parsed.sections {
		if !isRequired(section.name) {
			continue
		}
		content := section.content()
		switch {
		case content == "":
			problems = append(problems, "Пустой обязательный раздел: ## "+section.name)
		case isPlaceholder(content):
			problems = append(problems, "Раздел содержит только заглушку: ## "+section.name)
		}
	}
	return append(problems, hardWrapProblems(parsed)...)
}

// hardWrapProblems сообщает о многострочных абзацах обычного текста,
// выделенных при разборе блоков Markdown.
func hardWrapProblems(body parsed) []string {
	var problems []string
	if wrapped(body.preamble) {
		problems = append(problems, "Жёсткий перенос строки в тексте перед первым разделом: "+wrapHint)
	}
	for _, current := range body.sections {
		if wrapped(current.runs) {
			problems = append(problems, "Жёсткий перенос строки в разделе ## "+current.name+": "+wrapHint)
		}
	}
	return problems
}

// wrapped сообщает, есть ли среди абзацев хотя бы один многострочный.
func wrapped(runs []int) bool {
	for _, length := range runs {
		if length > 1 {
			return true
		}
	}
	return false
}

func headingList(names []string) string {
	return "## " + strings.Join(names, ", ## ")
}

func countOf(names []string, wanted string) int {
	count := 0
	for _, name := range names {
		if name == wanted {
			count++
		}
	}
	return count
}

func isRequired(name string) bool {
	return slices.Contains(requiredSections, name)
}

// orderProblems сообщает о первом обязательном разделе, который стоит раньше
// предыдущего обязательного раздела.
func orderProblems(names []string) []string {
	previousName := ""
	previousPosition := -1
	for _, required := range requiredSections {
		position := -1
		for i, name := range names {
			if name == required {
				position = i
				break
			}
		}
		if position < previousPosition {
			return []string{fmt.Sprintf(
				"Нарушен порядок разделов: ## %s расположен раньше, чем ## %s", required, previousName)}
		}
		previousName, previousPosition = required, position
	}
	return nil
}

// isPlaceholder принимает за заглушку очевидный шаблон: одна из известных
// фраз, целиком шаблон вида <причина> или набор таких строк. Маркер пункта
// списка сам по себе содержанием не является, поэтому список из одних заглушек
// тоже считается заглушкой.
func isPlaceholder(content string) bool {
	lines := strings.Split(content, "\n")
	nonEmpty := 0
	allPlaceholders := true
	for _, line := range lines {
		token := normalizeToken(line)
		if token == "" {
			continue
		}
		nonEmpty++
		if !placeholderToken(token) {
			allPlaceholders = false
		}
	}
	if nonEmpty == 0 || allPlaceholders {
		return true
	}
	return placeholderToken(normalizeToken(strings.Join(lines, " ")))
}

// normalizeToken приводит строку к сравнимому виду: снимает маркер пункта
// списка и разметку, затем нижний регистр, сжатые пробелы и завершающую
// пунктуацию.
func normalizeToken(text string) string {
	token := trimListItem(text)
	token = strings.Join(strings.Fields(token), " ")
	token = strings.TrimSpace(strings.Trim(token, "`*_~# \t"))
	token = strings.TrimSpace(strings.TrimRight(token, ".:;!,"))
	return strings.ToLower(token)
}

// trimListItem снимает ведущий маркер маркированного, нумерованного или
// списка задач вместе с отметкой выполнения.
func trimListItem(text string) string {
	rest := strings.TrimLeft(text, " \t")
	if marker := listMarkerLen(rest); marker > 0 {
		rest = strings.TrimLeft(rest[marker:], " \t")
	}
	for _, box := range []string{"[ ]", "[x]", "[X]"} {
		if strings.HasPrefix(rest, box) {
			return strings.TrimLeft(rest[len(box):], " \t")
		}
	}
	return rest
}

// listMarkerLen возвращает длину маркера пункта списка в начале строки:
// `- `, `* `, `+ `, `1. `, `1) `; ноль — маркера нет. Пробельный символ после
// маркера не обязателен: пустой пункт (`-`, `1.`) — тоже маркер, а не текст.
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

func placeholderToken(token string) bool {
	if placeholders[token] {
		return true
	}
	// Незаполненный шаблон-подсказка: <причина>, <что именно>.
	return len(token) > 2 && strings.HasPrefix(token, "<") && strings.HasSuffix(token, ">") &&
		!strings.ContainsAny(token[1:len(token)-1], "<>")
}

// normalizeNewlines — единственный владелец нормализации переводов строк в
// цепочке проверки: результат обязан быть до подсчёта лимита и до разбора
// разделов.
func normalizeNewlines(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return strings.ReplaceAll(body, "\r", "\n")
}

// section — видимый заголовок второго уровня и его видимый текст.
type section struct {
	name  string
	lines []string
	// runs хранит длины абзацев обычного текста, найденных в этом разделе:
	// абзац длиной больше одной строки — жёсткий перенос.
	runs []int
}

func (s section) content() string {
	return strings.Join(s.lines, "\n")
}

// parsed — результат разбора описания: преамбула перед первым видимым `##` и
// найденные разделы. Преамбула не участвует в проверке разделов, но участвует в
// проверке абзацев: перенос в ней — такое же нарушение формы.
type parsed struct {
	preamble []int
	sections []section
}

// fence описывает активный ограждённый блок кода.
type fence struct {
	ch  byte // маркер ограждения: '`' или '~'
	n   int  // длина маркера открытия
	col int  // 0 — блок верхнего уровня; >0 — содержимое пункта списка
}

type parser struct {
	sections  []section
	preamble  []int
	fence     *fence
	comment   bool
	listCol   int // столбец начала содержимого текущего пункта списка
	inTable   bool
	paraLines int
	paraHead  string // первая строка абзаца может оказаться шапкой таблицы
}

// parseSections выделяет видимые заголовки `##`, видимый текст их разделов и
// длины абзацев обычного текста. Ожидает текст с уже нормализованными
// переводами строк (validate).
func parseSections(body string) parsed {
	p := &parser{}
	for line := range strings.SplitSeq(body, "\n") {
		p.line(line)
	}
	p.closePara()
	return parsed{preamble: p.preamble, sections: p.sections}
}

// closePara завершает текущий абзац и запоминает его длину.
func (p *parser) closePara() {
	if p.paraLines == 0 {
		return
	}
	if n := len(p.sections); n > 0 {
		p.sections[n-1].runs = append(p.sections[n-1].runs, p.paraLines)
	} else {
		p.preamble = append(p.preamble, p.paraLines)
	}
	p.paraLines = 0
	p.paraHead = ""
}

func (p *parser) line(line string) {
	if p.fence == nil {
		p.text(line)
		return
	}
	active := *p.fence
	if active.col > 0 && !blank(line) && indentWidth(line) < active.col {
		// Пункт списка закончился раньше закрывающего маркера.
		p.fence = nil
		p.text(line)
		return
	}
	if closesFence(line, active) {
		p.fence = nil
	}
}

// text определяет границу или продолжение текущего блока.
func (p *parser) text(line string) {
	if blank(line) {
		// Пустая строка может продолжать пункт списка.
		p.inTable = false
		p.closePara()
		return
	}
	pad := indentWidth(line)
	if pad < p.listCol {
		p.listCol = 0
	}
	inPara := p.paraLines > 0
	if !p.comment {
		if !inPara && pad >= p.listCol+4 {
			p.inTable = false
			return
		}
		// Маркер комментария в строке информации ограждения — литерал.
		opening, opens := openFence(line, p.listCol)
		if !opens {
			if col, start := listItem(line, p.listCol, inPara); col > 0 {
				if ch, length, ok := fenceMarker(line[start:]); ok {
					opening, opens = fence{ch: ch, n: length, col: col}, true
					p.listCol = col
				}
			}
		}
		if opens {
			p.fence = &opening
			p.inTable = false
			p.closePara()
			return
		}
	}
	line = stripComments(line, &p.comment)
	switch {
	case blank(line), quoted(line):
		p.inTable = false
		p.closePara()
		return
	case inPara && setextUnderline(line):
		p.paraLines = 0
		p.paraHead = ""
		p.appendLine(line)
		return
	case horizontalRule(line):
		p.inTable = false
		p.closePara()
		return
	}
	if col, _ := listItem(line, p.listCol, inPara); col > 0 {
		p.listCol = col
		p.inTable = false
		p.closePara()
		p.appendLine(line)
		return
	}
	// Абзац принадлежит разделу, который был открыт до заголовка.
	if atxHeadingAny(line) {
		p.inTable = false
		p.closePara()
		if name, isSection := atxHeading(line); isSection {
			p.sections = append(p.sections, section{name: name})
		} else {
			p.appendLine(line)
		}
		return
	}
	p.appendLine(line)
	if p.listCol > 0 || p.inTable {
		return
	}
	if p.paraLines == 1 && tableHeader(p.paraHead, line) {
		p.paraLines = 0
		p.paraHead = ""
		p.inTable = true
		return
	}
	if !inPara {
		p.paraHead = line
	}
	p.paraLines++
}

// appendLine добавляет видимую строку в текст текущего раздела; строки
// преамбулы перед первым `##` содержанием раздела не являются.
func (p *parser) appendLine(line string) {
	if n := len(p.sections); n > 0 {
		p.sections[n-1].lines = append(p.sections[n-1].lines, line)
	}
}

// stripComments убирает HTML-комментарии, включая многострочные и незакрытые:
// незакрытый комментарий скрывает остаток строки и следующие строки.
// Строчный код в обратных кавычках — литеральный контекст: `<!--` внутри него
// комментарий не открывает. Вызывается вне ограждённых блоков и блоков кода с
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
	for i := 0; i < len(line); i++ {
		switch line[i] {
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

// leadColumns возвращает ширину ведущего отступа и позицию первого байта после
// него. Табуляция считается равной четырём колонкам — так же, как в indentWidth,
// поэтому отступ не зависит от того, набран он пробелами или табуляцией.
func leadColumns(line string) (width, index int) {
	for index < len(line) {
		switch line[index] {
		case ' ':
			width++
		case '\t':
			width += 4
		default:
			return width, index
		}
		index++
	}
	return width, index
}

// openFence распознаёт открывающий маркер ограждённого блока. Допустимый отступ
// отсчитывается от начала содержимого текущего пункта списка, если блок ему
// принадлежит: вне списка это до трёх пробелов от начала строки, внутри пункта —
// до трёх пробелов от listCol. Так же, как closesFence, отступ не может быть
// абсолютным: при listCol = 4 строка с четырьмя пробелами — законное ограждение.
func openFence(line string, listCol int) (fence, bool) {
	pad, idx := leadColumns(line)
	base := 0
	if listCol > 0 && pad >= listCol {
		base = listCol
	}
	if pad-base > 3 {
		return fence{}, false
	}
	ch, length, ok := fenceMarker(line[idx:])
	if !ok {
		return fence{}, false
	}
	if base > 0 {
		return fence{ch: ch, n: length, col: base}, true
	}
	return fence{ch: ch, n: length}, true
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

// atxHeadingAny распознаёт границу блока, включая пустые заголовки ATX.
func atxHeadingAny(line string) bool {
	pad := leadingSpaces(line)
	if pad > 3 || pad == len(line) {
		return false
	}
	text := line[pad:]
	hashes := 0
	for hashes < len(text) && text[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 {
		return false
	}
	return hashes == len(text) || text[hashes] == ' ' || text[hashes] == '\t'
}

// setextUnderline распознаёт подчёркивание заголовка: строка только из `=` или
// только из `-` (с необязательным отступом до трёх пробелов). В GFM такая строка
// превращает предыдущий абзац в заголовок, поэтому она прерывает абзац, но сама
// его строкой не является.
func setextUnderline(line string) bool {
	pad := leadingSpaces(line)
	if pad > 3 {
		return false
	}
	text := strings.TrimRight(line[pad:], " \t")
	if text == "" {
		return false
	}
	ch := text[0]
	if ch != '=' && ch != '-' {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] != ch {
			return false
		}
	}
	return true
}

// tableHeader проверяет пару строк до переклассификации абзаца в таблицу.
func tableHeader(header, delimiter string) bool {
	if indentWidth(delimiter) > 3 {
		return false
	}
	cells := splitTableCells(delimiter)
	if len(cells) == 0 || len(cells) != len(splitTableCells(header)) {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		cell = strings.TrimPrefix(cell, ":")
		cell = strings.TrimSuffix(cell, ":")
		if cell == "" {
			return false
		}
		for i := 0; i < len(cell); i++ {
			if cell[i] != '-' {
				return false
			}
		}
	}
	return true
}

// splitTableCells делит строку таблицы по неэкранированным `|` и отбрасывает
// крайние пустые ячейки, которые появляются от внешних вертикальных черт.
func splitTableCells(text string) []string {
	var cells []string
	var current strings.Builder
	escaped := false
	for i := 0; i < len(text); i++ {
		switch ch := text[i]; {
		case escaped:
			current.WriteByte(ch)
			escaped = false
		case ch == '\\':
			current.WriteByte(ch)
			escaped = true
		case ch == '|':
			cells = append(cells, current.String())
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	cells = append(cells, current.String())
	if len(cells) > 0 && strings.TrimSpace(cells[0]) == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && strings.TrimSpace(cells[len(cells)-1]) == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// listItem возвращает столбец и байтовую позицию начала содержимого
// допустимого пункта. Нулевой столбец означает, что строка не открывает пункт.
func listItem(line string, parentCol int, inPara bool) (col, start int) {
	pad, index := leadColumns(line)
	if pad > parentCol+3 {
		return 0, 0
	}
	rest := line[index:]
	marker := listMarkerLen(rest)
	if marker == 0 {
		return 0, 0
	}
	width, spaces := leadColumns(rest[marker:])
	start = index + marker + spaces
	if inPara && (start == len(line) || marker > 1 && strings.TrimLeft(rest[:marker-1], "0") != "1") {
		return 0, 0
	}
	return pad + marker + width, start
}

// atxHeading распознаёт заголовок ровно второго уровня: отступ до трёх
// пробелов, `##` и обязательный пробельный символ. Корректные закрывающие `#`
// не входят в имя раздела; заголовки `#` и `###` разделами описания не считаются.
func atxHeading(line string) (string, bool) {
	pad := leadingSpaces(line)
	if pad > 3 || pad == len(line) {
		return "", false
	}
	text := line[pad:]
	if !strings.HasPrefix(text, "##") || strings.HasPrefix(text, "###") {
		return "", false
	}
	text = text[2:]
	if text == "" || (text[0] != ' ' && text[0] != '\t') {
		return "", false
	}
	text = strings.TrimRight(strings.TrimLeft(text, " \t"), " \t")
	hashes := len(text)
	for hashes > 0 && text[hashes-1] == '#' {
		hashes--
	}
	if hashes < len(text) && hashes > 0 && (text[hashes-1] == ' ' || text[hashes-1] == '\t') {
		text = strings.TrimRight(text[:hashes], " \t")
	}
	return text, true
}

// quoted сообщает, что строка продолжает блок-цитату: её текст и заголовки
// невидимы для контракта.
func quoted(line string) bool {
	pad := leadingSpaces(line)
	return pad < 4 && pad < len(line) && line[pad] == '>'
}

// horizontalRule распознаёт горизонтальную линию из `-`, `*` или `_`.
func horizontalRule(line string) bool {
	pad := leadingSpaces(line)
	if pad > 3 {
		return false
	}
	text := strings.TrimSpace(line[pad:])
	if text == "" {
		return false
	}
	ch := text[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case ch:
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}
