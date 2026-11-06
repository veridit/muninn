package harness

import (
	"encoding/json"
	"errors"
	"io"
)

// streamSnapshot walks a jcode session snapshot ({"working_dir": ..., "messages": [...],
// ...}) token by token. Each message is decoded on its own and handed to fn, so
// memory is bounded by one message, not the file. Other keys are skipped.
func streamSnapshot(r io.Reader, fn func(captureStored)) (string, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return "", err
	}
	cwd := ""
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, ok := tok.(string)
		if !ok {
			return "", errors.New("snapshot: object key expected")
		}
		switch key {
		case "working_dir":
			var s *string
			if err := dec.Decode(&s); err != nil {
				return "", err
			}
			if s != nil {
				cwd = *s
			}
		case "messages":
			t, err := dec.Token()
			if err != nil {
				return "", err
			}
			if t == nil {
				continue // "messages": null
			}
			if d, ok := t.(json.Delim); !ok || d != '[' {
				return "", errors.New("snapshot: messages must be an array")
			}
			for dec.More() {
				var m captureStored
				if err := dec.Decode(&m); err != nil {
					return "", err
				}
				fn(m)
			}
			if err := expectDelim(dec, ']'); err != nil {
				return "", err
			}
		default:
			if err := skipValue(dec); err != nil {
				return "", err
			}
		}
	}
	return cwd, expectDelim(dec, '}')
}

// skipValue consumes one JSON value token by token, so large unknown fields
// are never held in memory as a whole.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != want {
		return errors.New("snapshot: unexpected token")
	}
	return nil
}
