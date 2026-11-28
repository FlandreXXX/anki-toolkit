package crowdanki

import (
	"context"
	"encoding/json/jsontext"
)

type fieldLayout struct {
	ordered []FieldDef
	byName  map[string]int
	code    Code
	offset  int64
}

func layout(ctx context.Context, model NoteModel) fieldLayout {
	fields := model.Fields()
	if fields.Kind() == jsontext.KindInvalid {
		return fieldLayout{code: Missing, offset: model.Value().Offset()}
	}
	if fields.Kind() != jsontext.KindBeginArray {
		return fieldLayout{code: Invalid, offset: model.Property("flds").Offset()}
	}
	l := fieldLayout{ordered: make([]FieldDef, fields.Len()), byName: make(map[string]int, fields.Len())}
	for _, field := range fields.All {
		if ctx.Err() != nil {
			break
		}
		n, ordOK := field.Ord().Integer()
		name, nameOK := field.Name().String()
		if !ordOK || n < 0 || n >= int64(fields.Len()) || !nameOK {
			return fieldLayout{code: Invalid, offset: field.Value().Offset()}
		}
		if _, exists := l.byName[name]; exists || l.ordered[n].Value().object != nil {
			return fieldLayout{code: Ambiguous, offset: field.Value().Offset()}
		}
		l.ordered[n] = field
		l.byName[name] = int(n)
	}
	return l
}

func (d *Document) fieldLayout(model NoteModel) (fieldLayout, error) {
	if !d.owns(model.Object, shapeModel) || !matchesTag(model, "NoteModel") {
		return fieldLayout{}, failure(Invalid, model.Value().Offset(), nil)
	}
	layout, ok := d.index.fields[model.Value().object]
	if !ok {
		return fieldLayout{}, failure(Invalid, model.Value().Offset(), nil)
	}
	if layout.code != "" {
		return fieldLayout{}, failure(layout.code, layout.offset, nil)
	}
	return layout, nil
}

// OrderedFields возвращает отдельный срез определений в порядке ord.
func (d *Document) OrderedFields(model NoteModel) ([]FieldDef, error) {
	l, err := d.fieldLayout(model)
	if err != nil {
		return nil, err
	}
	return append([]FieldDef(nil), l.ordered...), nil
}

type ResolvedField struct {
	Definition FieldDef
	Ord        int
	Value      Value
}

func (d *Document) noteLayout(note Note) (fieldLayout, error) {
	found, err := d.ResolveModel(note)
	if err != nil {
		return fieldLayout{}, err
	}
	l, err := d.fieldLayout(found.Entity)
	if err != nil {
		return fieldLayout{}, err
	}
	fields := note.Fields()
	switch {
	case fields.Kind() == jsontext.KindInvalid:
		return fieldLayout{}, failure(Missing, note.Value().Offset(), nil)
	case fields.Kind() != jsontext.KindBeginArray || fields.Len() != len(l.ordered):
		return fieldLayout{}, failure(Invalid, note.Property("fields").Offset(), nil)
	}
	return l, nil
}

func (d *Document) ResolveField(note Note, name string) (ResolvedField, error) {
	l, err := d.noteLayout(note)
	if err != nil {
		return ResolvedField{}, err
	}
	ord, ok := l.byName[name]
	if !ok {
		return ResolvedField{}, failure(Unresolved, note.Value().Offset(), nil)
	}
	return resolvedField(note, l, ord)
}

func (d *Document) ResolveFieldOrd(note Note, ord int) (ResolvedField, error) {
	l, err := d.noteLayout(note)
	if err != nil {
		return ResolvedField{}, err
	}
	if ord < 0 || ord >= len(l.ordered) {
		return ResolvedField{}, failure(Invalid, note.Value().Offset(), nil)
	}
	return resolvedField(note, l, ord)
}

func resolvedField(note Note, l fieldLayout, ord int) (ResolvedField, error) {
	value, _ := note.Fields().At(ord)
	if value.Kind() != jsontext.KindString {
		return ResolvedField{}, failure(Invalid, value.Offset(), nil)
	}
	return ResolvedField{Definition: l.ordered[ord], Ord: ord, Value: value}, nil
}
