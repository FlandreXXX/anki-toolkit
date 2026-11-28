package crowdanki

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
)

// Declaration указывает сущность и колоду, в которой она объявлена.
type Declaration[T any] struct {
	Deck   Deck
	Entity T
}

type group[T any] struct {
	all        []Declaration[T]
	equivalent bool
}

type directory[T any] map[string]group[T]

type index struct {
	decks   directory[Deck]
	notes   directory[Note]
	models  directory[NoteModel]
	configs directory[DeckConfig]
	fields  map[*object]fieldLayout
}

// Walk обходит корректно представимые объекты колод в исходном порядке,
// начиная с корня. Повреждённые позиции доступны отдельно через Children.
func (d *Document) Walk(yield func(Deck) bool) {
	walk(d.root, yield)
}

func walk(deck Deck, yield func(Deck) bool) bool {
	if !yield(deck) {
		return false
	}
	for _, child := range deck.Children().All {
		if child.Value().object != nil && !walk(child, yield) {
			return false
		}
	}
	return true
}

func (d *Document) buildIndex(ctx context.Context) error {
	d.index = index{make(directory[Deck]), make(directory[Note]), make(directory[NoteModel]),
		make(directory[DeckConfig]), make(map[*object]fieldLayout)}
	d.Walk(func(deck Deck) bool {
		if ctx.Err() != nil {
			return false
		}
		i := &d.index
		add(i.decks, deck, deck, deck.UUID(), true)
		for _, note := range deck.Notes().All {
			if ctx.Err() != nil {
				return false
			}
			add(i.notes, deck, note, note.GUID(), false)
		}
		for _, model := range deck.Models().All {
			if ctx.Err() != nil {
				return false
			}
			if model.Value().object != nil {
				i.fields[model.Value().object] = layout(ctx, model)
				add(i.models, deck, model, model.UUID(), true)
			}
		}
		for _, config := range deck.Configs().All {
			if ctx.Err() != nil {
				return false
			}
			add(i.configs, deck, config, config.UUID(), true)
		}
		return true
	})
	compareDefinitions(ctx, d.index.models)
	compareDefinitions(ctx, d.index.configs)
	if err := ctx.Err(); err != nil {
		return failure(Canceled, -1, err)
	}
	return nil
}

type entity interface {
	Value() Value
	TypeTag() Value
}

func add[T entity](dir directory[T], deck Deck, value T, id Value, uuid bool) {
	key, code := identity(id, uuid)
	if code == "" {
		g := dir[key]
		g.all = append(g.all, Declaration[T]{Deck: deck, Entity: value})
		dir[key] = g
	}
}

func compareDefinitions[T entity](ctx context.Context, dir directory[T]) {
	for key, g := range dir {
		if ctx.Err() != nil {
			return
		}
		if len(g.all) < 2 {
			continue
		}
		first, ok := comparableJSON(g.all[0].Entity.Value())
		if !ok {
			continue
		}
		g.equivalent = true
		for _, found := range g.all[1:] {
			if ctx.Err() != nil {
				return
			}
			raw, ok := comparableJSON(found.Entity.Value())
			if !ok || !bytes.Equal(first, raw) {
				g.equivalent = false
				break
			}
		}
		dir[key] = g
	}
}

func comparableJSON(v Value) (jsontext.Value, bool) {
	raw := jsontext.Value(v.Raw())
	err := raw.Format(jsontext.ReorderRawObjects(true), jsontext.PreserveRawStrings(false))
	return raw, err == nil
}

func identity(v Value, uuid bool) (string, Code) {
	if v.Kind() == jsontext.KindInvalid {
		return "", Missing
	}
	key, ok := v.String()
	if !ok || key == "" || (uuid && !validUUID(key)) {
		return "", Invalid
	}
	return key, ""
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

func matchesTag(t entity, expected string) bool {
	v := t.TypeTag()
	tag, ok := v.String()
	return v.Kind() == jsontext.KindInvalid || (ok && tag == expected)
}

func (d *Document) DeckDeclarations(uuid string) []Declaration[Deck] {
	return matches(d.index.decks, uuid)
}

func (d *Document) NoteDeclarations(guid string) []Declaration[Note] {
	return matches(d.index.notes, guid)
}

func (d *Document) ModelDeclarations(uuid string) []Declaration[NoteModel] {
	return matches(d.index.models, uuid)
}

func (d *Document) ConfigDeclarations(uuid string) []Declaration[DeckConfig] {
	return matches(d.index.configs, uuid)
}

func matches[T any](dir directory[T], key string) []Declaration[T] {
	return append([]Declaration[T](nil), dir[key].all...)
}

func lookup[T entity](dir directory[T], key, tag string, uuid bool, offset int64) (Declaration[T], error) {
	var zero Declaration[T]
	if key == "" || (uuid && !validUUID(key)) {
		return zero, failure(Invalid, offset, nil)
	}
	g := dir[key]
	switch {
	case len(g.all) == 0:
		return zero, failure(Unresolved, offset, nil)
	case len(g.all) > 1 && !g.equivalent:
		return zero, failure(Ambiguous, offset, nil)
	case !matchesTag(g.all[0].Entity, tag):
		return zero, failure(Invalid, g.all[0].Entity.TypeTag().Offset(), nil)
	default:
		return g.all[0], nil
	}
}

func (d *Document) FindDeck(uuid string) (Declaration[Deck], error) {
	return lookup(d.index.decks, uuid, "Deck", true, -1)
}

func (d *Document) FindNote(guid string) (Declaration[Note], error) {
	return lookup(d.index.notes, guid, "Note", false, -1)
}

func (d *Document) FindModel(uuid string) (Declaration[NoteModel], error) {
	return lookup(d.index.models, uuid, "NoteModel", true, -1)
}

func (d *Document) FindConfig(uuid string) (Declaration[DeckConfig], error) {
	return lookup(d.index.configs, uuid, "DeckConfig", true, -1)
}

func (d *Document) owns(o Object, s shape) bool {
	return o.value.object != nil && o.value.object.doc == d && o.value.object.shape == s
}

func (d *Document) ResolveModel(note Note) (Declaration[NoteModel], error) {
	if !d.owns(note.Object, shapeNote) || !matchesTag(note, "Note") {
		return Declaration[NoteModel]{}, failure(Invalid, note.Value().Offset(), nil)
	}
	return resolve(d.index.models, note.ModelUUID(), "NoteModel", note.Value().Offset())
}

func (d *Document) ResolveConfig(deck Deck) (Declaration[DeckConfig], error) {
	if !d.owns(deck.Object, shapeDeck) || !matchesTag(deck, "Deck") {
		return Declaration[DeckConfig]{}, failure(Invalid, deck.Value().Offset(), nil)
	}
	return resolve(d.index.configs, deck.ConfigUUID(), "DeckConfig", deck.Value().Offset())
}

func resolve[T entity](dir directory[T], ref Value, tag string, ownerOffset int64) (Declaration[T], error) {
	key, code := identity(ref, true)
	switch code {
	case Missing:
		return Declaration[T]{}, failure(code, ownerOffset, nil)
	case Invalid:
		return Declaration[T]{}, failure(code, ref.Offset(), nil)
	}
	return lookup(dir, key, tag, true, ref.Offset())
}
