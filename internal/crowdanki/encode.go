package crowdanki

import "context"

// Encode собирает полный JSON из представления документа. Порядок
// свойств и массивов детерминирован, а непрозрачные значения идут целиком.
func (d *Document) Encode(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, failure(Canceled, -1, err)
	}
	if d.root.Value().object == nil {
		return nil, failure(Schema, -1, nil)
	}
	out, err := appendValue(ctx, make([]byte, 0, len(d.source)), d.root.Value())
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, failure(Canceled, -1, err)
	}
	return out, nil
}

func appendValue(ctx context.Context, out []byte, v Value) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, failure(Canceled, v.offset, err)
	}
	switch {
	case v.object != nil:
		out = append(out, '{')
		for i, p := range v.object.properties {
			if i != 0 {
				out = append(out, ',')
			}
			out = append(out, p.key...)
			out = append(out, ':')
			var err error
			out, err = appendValue(ctx, out, p.val)
			if err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil
	case v.items != nil:
		out = append(out, '[')
		for i, item := range v.items {
			if i != 0 {
				out = append(out, ',')
			}
			var err error
			out, err = appendValue(ctx, out, item)
			if err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil
	default:
		return append(out, v.raw...), nil
	}
}
