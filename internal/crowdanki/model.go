// Package crowdanki представляет один неизменяемый документ CrowdAnki.
package crowdanki

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
)

// Value сохраняет состояние, тип и исходное представление значения JSON.
// Нулевое значение соответствует отсутствующему свойству.
type Value struct {
	raw    []byte
	object *object
	items  []Value
	offset int64
}

func (v Value) Kind() jsontext.Kind { return jsontext.Value(v.raw).Kind() }
func (v Value) Raw() []byte         { return bytes.Clone(v.raw) }
func (v Value) Offset() int64       { return v.offset }

func (v Value) String() (string, bool) {
	if v.Kind() != jsontext.KindString {
		return "", false
	}
	var s string
	err := json.Unmarshal(v.raw, &s)
	return s, err == nil
}

func (v Value) Integer() (int64, bool) {
	if v.Kind() != jsontext.KindNumber {
		return 0, false
	}
	n, err := strconv.ParseInt(string(v.raw), 10, 64)
	return n, err == nil
}

type property struct {
	name string
	key  []byte
	val  Value
}

type object struct {
	doc        *Document
	shape      shape
	properties []property
}

// Object даёт доступ ко всем свойствам сущности, включая неизвестные.
type Object struct{ value Value }

func (o Object) Value() Value { return o.value }

func (o Object) Property(name string) Value {
	if o.value.object != nil {
		for _, p := range o.value.object.properties {
			if p.name == name {
				return p.val
			}
		}
	}
	return Value{}
}

func (o Object) TypeTag() Value { return o.Property("__type__") }
func (o Object) Name() Value    { return o.Property("name") }

// List сохраняет порядок и позиции повреждённых элементов. Kind показывает
// состояние самого свойства, даже когда Len равен нулю.
type List[T any] struct {
	value Value
	wrap  func(Value) T
}

func (l List[T]) Kind() jsontext.Kind { return l.value.Kind() }
func (l List[T]) Len() int            { return len(l.value.items) }

func (l List[T]) At(i int) (T, bool) {
	if i >= 0 && i < l.Len() {
		return l.wrap(l.value.items[i]), true
	}
	var zero T
	return zero, false
}

func (l List[T]) All(yield func(int, T) bool) {
	for i, v := range l.value.items {
		if !yield(i, l.wrap(v)) {
			break
		}
	}
}

func entities[T any](v Value, wrap func(Value) T) List[T] {
	return List[T]{value: v, wrap: wrap}
}

func values(v Value) List[Value] {
	return List[Value]{value: v, wrap: func(v Value) Value { return v }}
}

type Deck struct{ Object }
type Note struct{ Object }
type NoteModel struct{ Object }
type FieldDef struct{ Object }
type TemplateDef struct{ Object }
type DeckConfig struct{ Object }

func (d Deck) UUID() Value       { return d.Property("crowdanki_uuid") }
func (d Deck) ConfigUUID() Value { return d.Property("deck_config_uuid") }
func (d Deck) Children() List[Deck] {
	return entities(d.Property("children"), asDeck)
}
func asDeck(v Value) Deck { return Deck{Object{v}} }

func (d Deck) Notes() List[Note] {
	return entities(d.Property("notes"), func(v Value) Note { return Note{Object{v}} })
}
func (d Deck) Models() List[NoteModel] {
	return entities(d.Property("note_models"), func(v Value) NoteModel { return NoteModel{Object{v}} })
}
func (d Deck) Configs() List[DeckConfig] {
	return entities(d.Property("deck_configurations"), func(v Value) DeckConfig { return DeckConfig{Object{v}} })
}
func (d Deck) Media() List[Value] { return values(d.Property("media_files")) }

func (n Note) GUID() Value         { return n.Property("guid") }
func (n Note) ModelUUID() Value    { return n.Property("note_model_uuid") }
func (n Note) Fields() List[Value] { return values(n.Property("fields")) }
func (n Note) Tags() List[Value]   { return values(n.Property("tags")) }

type ModelType int64

const (
	Standard ModelType = 0
	Cloze    ModelType = 1
)

func (m NoteModel) UUID() Value         { return m.Property("crowdanki_uuid") }
func (m NoteModel) CSS() Value          { return m.Property("css") }
func (m NoteModel) Requirements() Value { return m.Property("req") }

func (m NoteModel) ModelType() (ModelType, bool) {
	n, ok := m.Property("type").Integer()
	t := ModelType(n)
	return t, ok && (t == Standard || t == Cloze)
}

func (m NoteModel) Fields() List[FieldDef] {
	return entities(m.Property("flds"), func(v Value) FieldDef { return FieldDef{Object{v}} })
}
func (m NoteModel) Templates() List[TemplateDef] {
	return entities(m.Property("tmpls"), func(v Value) TemplateDef { return TemplateDef{Object{v}} })
}

func (f FieldDef) Ord() Value         { return f.Property("ord") }
func (f FieldDef) Description() Value { return f.Property("description") }

func (t TemplateDef) Ord() Value      { return t.Property("ord") }
func (t TemplateDef) Question() Value { return t.Property("qfmt") }
func (t TemplateDef) Answer() Value   { return t.Property("afmt") }

func (c DeckConfig) UUID() Value { return c.Property("crowdanki_uuid") }
