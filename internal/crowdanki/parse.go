package crowdanki

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
)

// Limits задаёт верхние границы чтения и построения представлений.
// Ноль выбирает значение по умолчанию независимо для каждого ограничения.
type Limits struct {
	MaxBytes   int64
	MaxDepth   int
	MaxEntries int
}

const (
	DefaultMaxBytes   = 64 << 20
	DefaultMaxDepth   = 128
	DefaultMaxEntries = 1 << 20
	maxSupportedDepth = 1024
)

// Document владеет байтами и всеми представлениями одного экспорта.
type Document struct {
	source []byte
	root   Deck
	index  index
}

func (d *Document) Root() Deck       { return d.root }
func (d *Document) Original() []byte { return bytes.Clone(d.source) }

// Parse читает ровно один экспорт. Чтение ограничено MaxBytes+1 байтами.
func Parse(ctx context.Context, src io.Reader, limits Limits) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, failure(Canceled, -1, err)
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = DefaultMaxBytes
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = DefaultMaxDepth
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = DefaultMaxEntries
	}
	if limits.MaxBytes < 0 || limits.MaxBytes >= math.MaxInt || limits.MaxDepth < 0 || limits.MaxDepth > maxSupportedDepth || limits.MaxEntries < 0 {
		return nil, failure(Limit, -1, nil)
	}
	reader := &io.LimitedReader{R: &checkedReader{ctx: ctx, src: src}, N: limits.MaxBytes + 1}
	data, err := io.ReadAll(reader)
	if cancel := ctx.Err(); cancel != nil {
		return nil, failure(Canceled, -1, cancel)
	}
	if reader.N == 0 {
		return nil, failure(Limit, limits.MaxBytes, nil)
	}
	if err != nil {
		return nil, failure(Read, -1, err)
	}
	doc := &Document{source: data}
	p := parser{
		ctx: ctx, doc: doc, dec: jsontext.NewDecoder(bytes.NewBuffer(data)),
		maxDepth: limits.MaxDepth, remaining: limits.MaxEntries,
	}
	root, err := p.parseValue(shapeDeck)
	if err != nil {
		return nil, err
	}
	if _, err = p.token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = failure(Syntax, p.dec.InputOffset(), nil)
		}
		return nil, err
	}
	doc.root = asDeck(root)
	tag, _ := doc.root.TypeTag().String()
	if root.Kind() != jsontext.KindBeginObject || tag != "Deck" {
		return nil, failure(Schema, root.Offset(), nil)
	}
	if err := doc.buildIndex(ctx); err != nil {
		return nil, err
	}
	return doc, nil
}

type checkedReader struct {
	ctx   context.Context
	src   io.Reader
	empty int
}

func (r *checkedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.src.Read(p)
	if n != 0 || err != nil {
		r.empty = 0
	} else {
		r.empty++
		if r.empty >= 100 {
			return 0, io.ErrNoProgress
		}
	}
	return n, err
}

type shape int

const (
	shapeRaw shape = iota
	shapeDeck
	shapeNote
	shapeModel
	shapeObject
	shapeArray
	shapeDecks
	shapeNotes
	shapeModels
	shapeObjects
)

func (s shape) member(name string) shape {
	switch s {
	case shapeDeck:
		switch name {
		case "children":
			return shapeDecks
		case "notes":
			return shapeNotes
		case "note_models":
			return shapeModels
		case "deck_configurations":
			return shapeObjects
		case "media_files":
			return shapeArray
		}
	case shapeNote:
		if name == "fields" || name == "tags" {
			return shapeArray
		}
	case shapeModel:
		if name == "flds" || name == "tmpls" {
			return shapeObjects
		}
	}
	return shapeRaw
}

func (s shape) element() shape {
	switch s {
	case shapeDecks:
		return shapeDeck
	case shapeNotes:
		return shapeNote
	case shapeModels:
		return shapeModel
	case shapeObjects:
		return shapeObject
	default:
		return shapeRaw
	}
}

type parser struct {
	ctx       context.Context
	doc       *Document
	dec       *jsontext.Decoder
	maxDepth  int
	remaining int
}

func (p *parser) token() (jsontext.Token, error) {
	if err := p.ctx.Err(); err != nil {
		return jsontext.Token{}, failure(Canceled, p.dec.InputOffset(), err)
	}
	tok, err := p.dec.ReadToken()
	if err != nil && err != io.EOF {
		offset := p.dec.InputOffset()
		var semantic *jsontext.SyntacticError
		if errors.As(err, &semantic) {
			offset = semantic.ByteOffset
		}
		return tok, failure(Syntax, offset, err)
	}
	if p.dec.StackDepth() > p.maxDepth {
		return tok, failure(Limit, p.dec.InputOffset()-1, nil)
	}
	return tok, err
}

func (p *parser) required() (jsontext.Token, error) {
	tok, err := p.token()
	if err == io.EOF {
		err = failure(Syntax, p.dec.InputOffset(), io.ErrUnexpectedEOF)
	}
	return tok, err
}

func (p *parser) entry() error {
	if p.remaining == 0 {
		return failure(Limit, p.dec.InputOffset(), nil)
	}
	p.remaining--
	return nil
}

func (p *parser) parseValue(s shape) (Value, error) {
	before := p.dec.InputOffset()
	tok, err := p.required()
	if err != nil {
		return Value{}, err
	}
	return p.value(s, tok, before)
}

// value достраивает значение, первый токен которого уже прочитан. Разделение
// позволяет списать бюджет только после подтверждённого начала элемента:
// оборванный поток даёт syntax, а не limit.
func (p *parser) value(s shape, tok jsontext.Token, before int64) (Value, error) {
	start := p.start(before)
	v := Value{offset: start}
	var err error
	switch {
	case tok.Kind() == jsontext.KindBeginObject && s > shapeRaw && s < shapeArray:
		v.object, err = p.parseObject(s)
	case tok.Kind() == jsontext.KindBeginArray && s >= shapeArray:
		v.items, err = p.parseArray(s.element())
	default:
		// Токены внутри непрозрачного значения тоже проходят строгую
		// проверку глубины, Unicode и повторяющихся имён.
		if tok.Kind() == jsontext.KindBeginObject || tok.Kind() == jsontext.KindBeginArray {
			depth := p.dec.StackDepth()
			for p.dec.StackDepth() >= depth && err == nil {
				_, err = p.required()
			}
		}
	}
	if err != nil {
		return Value{}, err
	}
	v.raw = p.doc.source[start:p.dec.InputOffset()]
	return v, nil
}

func (p *parser) parseObject(s shape) (*object, error) {
	o := &object{doc: p.doc, shape: s}
	for p.dec.PeekKind() != jsontext.KindEndObject {
		before := p.dec.InputOffset()
		key, err := p.required()
		if err != nil {
			return nil, err
		}
		if err := p.entry(); err != nil {
			return nil, err
		}
		name := key.String()
		raw := p.doc.source[p.start(before):p.dec.InputOffset()]
		val, err := p.parseValue(s.member(name))
		if err != nil {
			return nil, err
		}
		o.properties = append(o.properties, property{name: name, key: raw, val: val})
	}
	_, err := p.required()
	return o, err
}

func (p *parser) parseArray(element shape) ([]Value, error) {
	items := make([]Value, 0)
	for p.dec.PeekKind() != jsontext.KindEndArray {
		before := p.dec.InputOffset()
		tok, err := p.required()
		if err != nil {
			return nil, err
		}
		// Бюджет списывается за подтверждённый элемент, а не за
		// непрочитанный токен: оборванный ввод даёт syntax, не limit.
		if err := p.entry(); err != nil {
			return nil, err
		}
		v, err := p.value(element, tok, before)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	_, err := p.required()
	return items, err
}

// start пропускает только разделители перед уже проверенным токеном.
func (p *parser) start(before int64) int64 {
	for i := before; i < p.dec.InputOffset(); i++ {
		switch p.doc.source[i] {
		case ' ', '\t', '\r', '\n', ',', ':':
		default:
			return i
		}
	}
	return p.dec.InputOffset()
}
