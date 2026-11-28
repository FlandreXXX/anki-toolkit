package crowdanki_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	ca "github.com/FlandreXXX/anki-toolkit/internal/crowdanki"
)

const modelID = "11111111-1111-1111-1111-111111111111"
const clozeID = "22222222-2222-2222-2222-222222222222"
const configID = "33333333-3333-3333-3333-333333333333"
const deckID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const childID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

const fixture = ` {
	"__type__": "Deck",
	"name": "Контейнер",
	"crowdanki_uuid": "` + deckID + `",
	"deck_config_uuid": "` + configID + `",
	"Notes": "регистр",
	"unknown": {"empty":{},"zero":0,"disabled":false,"huge":900719925474099312345,
	            "fraction":1.234567890123456789,"exp":-0.10E+900,"empty_text":"","unicode":"e\u0301"},
	"notes": [],
	"media_files": ["root.png", "root.png"],
	"deck_configurations": [{"__type__":"DeckConfig","crowdanki_uuid":"` + configID + `","name":"План",
	                          "new":{"delays":[0,2.00],"other":null}}],
	"note_models": [
		{"__type__":"NoteModel","crowdanki_uuid":"` + modelID + `","name":"Общая","type":0,
		 "flds":[{"name":"Оборот","ord":1,"description":null,"other":{"k":false}},
		         {"name":"Лицо","ord":0,"description":"пример"}],
		 "tmpls":[{"name":"Один","ord":0,"qfmt":"<b>{{Лицо}}</b>","afmt":"{{Оборот}}","other":[]}],
		 "css":"b { color:red; }","req":[[0,"all",[0]]],"extension":null},
		{"__type__":"NoteModel","crowdanki_uuid":"` + clozeID + `","name":"Общая","type":1,
		 "flds":[{"name":"Фраза","ord":0}],"tmpls":[{"name":"Пропуск","ord":0,"qfmt":"{{cloze:Фраза}}"}]}
	],
	"children":[
		{"name":"другое::имя","crowdanki_uuid":"` + childID + `","notes":[
			{"__type__":"Note","guid":"!:{♥}","note_model_uuid":"` + modelID + `",
			 "fields":["<b> e\u0301 </b>",""],"tags":["  Тег ","  Тег "],"other":[]},
			{"guid":"cloze","note_model_uuid":"` + clozeID + `","fields":["{{c1::слово}}"],"tags":null},
			null, {"guid":false,"fields":[null,5,[]],"tags":"ошибка"}
		],"children":[{"name":"Лист","notes":null,"media_files":["child.gif",false]}]},
		{"name":"Лист","notes":{},"media_files":null},
		null
	]
}
`

func parse(t testing.TB, input string) *ca.Document {
	t.Helper()
	doc, err := ca.Parse(context.Background(), strings.NewReader(input), ca.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func requireCode(t testing.TB, err error, code ca.Code) {
	t.Helper()
	var e *ca.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("ошибка %v, ожидался код %s", err, code)
	}
}

func textValue(t *testing.T, v ca.Value, want string) {
	t.Helper()
	if got, ok := v.String(); !ok || got != want {
		t.Fatalf("значение %q (%v), ожидалось %q", got, ok, want)
	}
}

func textValues(t *testing.T, list ca.List[ca.Value]) string {
	t.Helper()
	var parts []string
	for _, v := range list.All {
		s, ok := v.String()
		if !ok {
			t.Fatalf("элемент не строка: %v", v.Kind())
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

func TestModelAndResolution(t *testing.T) {
	doc := parse(t, fixture)
	root := doc.Root()
	var order []string
	for deck := range doc.Walk {
		name, _ := deck.Name().String()
		order = append(order, name)
	}
	if !slices.Equal(order, []string{"Контейнер", "другое::имя", "Лист", "Лист"}) {
		t.Fatalf("порядок дерева %v", order)
	}
	child, err := doc.FindDeck(childID)
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := doc.FindNote("!:{♥}")
	if err != nil {
		t.Fatal(err)
	}
	if declaration.Deck.Value().Offset() != child.Entity.Value().Offset() {
		t.Fatal("потеряна непосредственная принадлежность заметки")
	}
	note := declaration.Entity
	model, err := doc.ResolveModel(note)
	if err != nil {
		t.Fatal(err)
	}
	textValue(t, model.Deck.UUID(), deckID)
	textValue(t, model.Entity.Name(), "Общая")
	if kind, ok := model.Entity.ModelType(); !ok || kind != ca.Standard {
		t.Fatal("ожидалась обычная модель")
	}
	other, err := doc.FindModel(clozeID)
	if err != nil {
		t.Fatal(err)
	}
	if kind, ok := other.Entity.ModelType(); !ok || kind != ca.Cloze {
		t.Fatal("ожидалась cloze-модель")
	}
	textValue(t, other.Entity.Name(), "Общая")
	template, _ := model.Entity.Templates().At(0)
	textValue(t, template.Question(), "<b>{{Лицо}}</b>")
	textValue(t, template.Answer(), "{{Оборот}}")
	textValue(t, model.Entity.CSS(), "b { color:red; }")
	if string(model.Entity.Requirements().Raw()) != `[[0,"all",[0]]]` {
		t.Fatal("изменён req")
	}
	field, err := doc.ResolveField(note, "Лицо")
	if err != nil || field.Ord != 0 {
		t.Fatalf("разрешение по имени: ord=%d, ошибка %v", field.Ord, err)
	}
	textValue(t, field.Value, "<b> e\u0301 </b>")
	field, err = doc.ResolveFieldOrd(note, 1)
	if err != nil || field.Ord != 1 {
		t.Fatalf("разрешение по позиции: ord=%d, ошибка %v", field.Ord, err)
	}
	textValue(t, field.Value, "")
	textValue(t, field.Definition.Name(), "Оборот")
	ordered, err := doc.OrderedFields(model.Entity)
	if err != nil || len(ordered) != 2 {
		t.Fatalf("порядок ord: %v", err)
	}
	textValue(t, ordered[0].Name(), "Лицо")
	ordered[0] = ca.FieldDef{}
	again, _ := doc.OrderedFields(model.Entity)
	textValue(t, again[0].Name(), "Лицо")
	first, _ := model.Entity.Fields().At(0)
	textValue(t, first.Name(), "Оборот")
	if first.Description().Kind() != jsontext.KindNull {
		t.Fatal("потеряно явное null")
	}
	if got := textValues(t, note.Tags()); got != "  Тег ,  Тег " {
		t.Fatalf("теги: %q", got)
	}
	config, err := doc.ResolveConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	textValue(t, config.Entity.Name(), "План")
	if raw := string(config.Entity.Property("new").Raw()); raw != `{"delays":[0,2.00],"other":null}` {
		t.Fatal("изменены параметры планировщика")
	}
	textValue(t, root.Property("Notes"), "регистр")
	if got := textValues(t, root.Media()); got != "root.png,root.png" {
		t.Fatalf("медиа корня: %q", got)
	}
	leaf, _ := child.Entity.Children().At(0)
	media, _ := leaf.Media().At(0)
	textValue(t, media, "child.gif")
	if leaf.Notes().Kind() != jsontext.KindNull || child.Entity.Media().Kind() != jsontext.KindInvalid {
		t.Fatal("смешаны отсутствующее свойство и null")
	}
	wrong, _ := root.Children().At(1)
	if wrong.Notes().Kind() != jsontext.KindBeginObject || wrong.Media().Kind() != jsontext.KindNull {
		t.Fatal("изменены типы повреждённых значений")
	}
	null, ok := child.Entity.Notes().At(2)
	if !ok || null.Value().Kind() != jsontext.KindNull {
		t.Fatal("потеряна позиция null")
	}
	copies := doc.NoteDeclarations("!:{♥}")
	copies[0] = ca.Declaration[ca.Note]{}
	if got := doc.NoteDeclarations("!:{♥}")[0].Entity; got.Value().Offset() != note.Value().Offset() {
		t.Fatal("вызывающий код изменил индекс")
	}

	otherDoc := parse(t, strings.Replace(fixture, "Контейнер", "Независимая", 1))
	_, err = otherDoc.ResolveModel(note)
	requireCode(t, err, ca.Invalid)
	owned, err := otherDoc.FindNote("!:{♥}")
	if err != nil {
		t.Fatal(err)
	}
	ownerModel, err := otherDoc.ResolveModel(owned.Entity)
	if err != nil {
		t.Fatal(err)
	}
	textValue(t, ownerModel.Deck.Name(), "Независимая")
	_, err = doc.ResolveModel(ca.Note{Object: first.Object})
	requireCode(t, err, ca.Invalid)
	_, err = doc.FindDeck(strings.ToUpper(deckID))
	requireCode(t, err, ca.Unresolved)
}

func decodeJSON(t testing.TB, data []byte) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if !utf8.Valid(data) {
		t.Fatal("JSON не в UTF-8")
	}
	v, err := referenceValue(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Token(); err != io.EOF {
		t.Fatalf("лишний ввод: %v", err)
	}
	return v
}

// Независимый декодер сохраняет json.Number и отвергает повторные имена.
func referenceValue(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		m := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("ожидалось имя JSON-свойства")
			}
			if _, exists := m[name]; exists {
				return nil, errors.New("повторное имя JSON-свойства")
			}
			v, err := referenceValue(d)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
		_, err = d.Token()
		return m, err
	case json.Delim('['):
		a := make([]any, 0)
		for d.More() {
			v, err := referenceValue(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err = d.Token()
		return a, err
	default:
		if _, unexpected := tok.(json.Delim); unexpected {
			return nil, errors.New("неожиданный разделитель JSON")
		}
		return tok, nil
	}
}

func assertRoundTrip(t testing.TB, doc *ca.Document, input []byte) []byte {
	t.Helper()
	ctx := context.Background()
	if !bytes.Equal(doc.Original(), input) {
		t.Fatal("исходный документ отличается побайтно")
	}
	encoded, err := doc.Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, input), decodeJSON(t, encoded)) {
		t.Fatal("содержимое или числовые лексемы изменились")
	}
	again := parse(t, string(encoded))
	second, err := again.Encode(ctx)
	if err != nil || !bytes.Equal(second, encoded) {
		t.Fatal("повторная сериализация отличается")
	}
	return encoded
}

func TestRoundTripAndOwnership(t *testing.T) {
	input := []byte(fixture)
	doc, err := ca.Parse(context.Background(), bytes.NewReader(input), ca.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	clear(input)
	raw := doc.Original()
	clear(raw)
	value := doc.Root().Property("unknown").Raw()
	clear(value)
	encoded := assertRoundTrip(t, doc, []byte(fixture))
	if bytes.Equal(encoded, []byte(fixture)) {
		t.Fatal("структурная сериализация совпала с форматированным исходником")
	}
	clear(encoded)
	assertRoundTrip(t, doc, []byte(fixture))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := doc.Encode(ctx)
	requireCode(t, err, ca.Canceled)
	if out != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("отмена вернула результат или потеряла причину")
	}
}

func TestStrictParse(t *testing.T) {
	cases := []struct {
		description string
		source      string
		code        ca.Code
	}{
		{"пустой ввод", "", ca.Syntax},
		{"обрезка", `{"__type__":"Deck","notes":[{} `, ca.Syntax},
		{"обрезка ключа", `{"__type__":"Deck",`, ca.Syntax},
		{"мусор", `{"__type__":"Deck"} !`, ca.Syntax},
		{"второй документ", `{"__type__":"Deck"} {}`, ca.Syntax},
		{"дубль корня", `{"__type__":"Deck","__type__":"Deck"}`, ca.Syntax},
		{"дубль в неизвестном объекте", `{"__type__":"Deck","x":[{"секрет":1,"\u0441екрет":2}]}`, ca.Syntax},
		{"дубль в типизированном объекте", `{"__type__":"Deck","notes":[{"guid":"секрет","guid":null}]}`, ca.Syntax},
		{"суррогат в значении", `{"__type__":"Deck","x":"\uD800"}`, ca.Syntax},
		{"суррогат в ключе", `{"__type__":"Deck","\uDC00":false}`, ca.Syntax},
		{"битый UTF-8", "{\"__type__\":\"Deck\",\"x\":\"\xff\"}", ca.Syntax},
		{"запятая у конца", `{"__type__":"Deck","children":[{},]}`, ca.Syntax},
		{"корень массив", `[]`, ca.Schema},
		{"тип корня", `{"__type__":"Note"}`, ca.Schema},
		{"регистр типа корня", `{"__TYPE__":"Deck"}`, ca.Schema},
		{"null вместо тега", `{"__type__":null}`, ca.Schema},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			doc, err := ca.Parse(context.Background(), strings.NewReader(tc.source), ca.Limits{})
			requireCode(t, err, tc.code)
			if doc != nil || strings.Contains(err.Error(), "секрет") {
				t.Fatal("частичный документ или приватные данные в ошибке")
			}
			if tc.code == ca.Syntax && !strings.Contains(err.Error(), "байт") {
				t.Fatal("нет безопасной позиции ошибки")
			}
		})
	}
}

func TestLimitChargesOnlyStartedEntry(t *testing.T) {
	// Оборванный элемент не должен превращать syntax в limit: бюджет
	// списывается только за подтверждённое начало.
	_, err := ca.Parse(context.Background(), strings.NewReader(`{"__type__":"Deck","notes":[true`), ca.Limits{MaxEntries: 3})
	requireCode(t, err, ca.Syntax)
	doc, err := ca.Parse(context.Background(), strings.NewReader(`{"__type__":"Deck","notes":[true]}`), ca.Limits{MaxEntries: 3})
	if err != nil || doc.Root().Notes().Len() != 1 {
		t.Fatalf("граница бюджета: %v", err)
	}
	_, err = ca.Parse(context.Background(), strings.NewReader(`{"__type__":"Deck","notes":[true,true]}`), ca.Limits{MaxEntries: 3})
	requireCode(t, err, ca.Limit)
}

func TestLimitsAndReader(t *testing.T) {
	input := `{"__type__":"Deck","x":[{"z":[]}]}`
	for _, tc := range []struct {
		lim  ca.Limits
		code ca.Code
	}{
		{ca.Limits{MaxBytes: int64(len(input)), MaxDepth: 4, MaxEntries: 2}, ""},
		{ca.Limits{MaxBytes: int64(len(input) - 1)}, ca.Limit},
		{ca.Limits{MaxDepth: 3}, ca.Limit},
		{ca.Limits{MaxDepth: 1025}, ca.Limit},
		{ca.Limits{MaxEntries: 1}, ca.Limit},
		{ca.Limits{MaxBytes: -1}, ca.Limit},
	} {
		doc, err := ca.Parse(context.Background(), iotest.OneByteReader(strings.NewReader(input)), tc.lim)
		if tc.code == "" {
			if err != nil || doc == nil {
				t.Fatalf("граничный ввод: %v", err)
			}
		} else {
			requireCode(t, err, tc.code)
		}
	}
	sensitive := errors.New("секрет источника")
	r := io.MultiReader(strings.NewReader(input), iotest.ErrReader(sensitive))
	_, err := ca.Parse(context.Background(), r, ca.Limits{})
	requireCode(t, err, ca.Read)
	if !errors.Is(err, sensitive) || strings.Contains(err.Error(), "секрет") {
		t.Fatal("причина должна быть доступна только через цепочку")
	}
	_, err = ca.Parse(context.Background(), emptyReader{}, ca.Limits{})
	requireCode(t, err, ca.Read)
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatal("источник без прогресса")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = ca.Parse(ctx, cancelReader{cancel}, ca.Limits{})
	requireCode(t, err, ca.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("потеряна причина отмены")
	}
	_, err = ca.Parse(ctx, iotest.ErrReader(sensitive), ca.Limits{})
	requireCode(t, err, ca.Canceled)
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read(b []byte) (int, error) {
	b[0] = '{'
	r.cancel()
	return 1, nil
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`{"__type__":"Deck"}`, `{"__type__":"Deck","children":[{"notes":[{"fields":[null,2,3E+99]}]}]}`, `{"__type__":"Deck","a":{"x":0,"x":1}}`, `{"__type__":"Deck","x":"\uD800"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 32<<10 {
			return
		}
		doc, err := ca.Parse(context.Background(), bytes.NewReader(input), ca.Limits{MaxBytes: 32 << 10, MaxDepth: 64})
		if err != nil {
			var typed *ca.Error
			if doc != nil || !errors.As(err, &typed) {
				t.Fatalf("нет атомарного типизированного отказа: %v", err)
			}
			return
		}
		assertRoundTrip(t, doc, input)
	})
}
