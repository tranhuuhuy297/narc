package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// jsonObject is a JSON object that keeps its keys in document order, so
// the settings files narc edits keep their layout. Values are *jsonObject,
// []any, string, json.Number, bool or nil.
type jsonObject struct {
	keys []string
	vals map[string]any
}

func newObject() *jsonObject { return &jsonObject{vals: map[string]any{}} }

// object builds a jsonObject from alternating keys and values.
func object(kv ...any) *jsonObject {
	o := newObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.set(kv[i].(string), kv[i+1])
	}
	return o
}

func (o *jsonObject) get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// set replaces a value in place or appends a new key.
func (o *jsonObject) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *jsonObject) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *jsonObject) str(k string) string {
	s, _ := o.vals[k].(string)
	return s
}

// childObject returns o[k] as an object, creating it when missing.
func (o *jsonObject) childObject(k string) (*jsonObject, error) {
	v, ok := o.get(k)
	if !ok || v == nil {
		c := newObject()
		o.set(k, c)
		return c, nil
	}
	c, ok := v.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%q is not a JSON object", k)
	}
	return c, nil
}

// childArray returns o[k] as an array (nil when missing).
func (o *jsonObject) childArray(k string) ([]any, error) {
	v, ok := o.get(k)
	if !ok || v == nil {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%q is not a JSON array", k)
	}
	return a, nil
}

// parseObject parses a JSON document whose top level is an object. Empty
// input is an empty object.
func parseObject(b []byte) (*jsonObject, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return newObject(), nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the top-level JSON value")
	}
	o, ok := v.(*jsonObject)
	if !ok {
		return nil, errors.New("top-level JSON value is not an object")
	}
	return o, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		o := newObject()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, _ := kt.(string)
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(k, v)
		}
		_, err = dec.Token() // '}'
		return o, err
	case '[':
		a := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err = dec.Token() // ']'
		return a, err
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", d)
}

// encodeDocument renders v with two-space indentation and a final newline.
func encodeDocument(v any) []byte {
	var b bytes.Buffer
	writeValue(&b, v, "")
	b.WriteByte('\n')
	return b.Bytes()
}

func writeValue(b *bytes.Buffer, v any, indent string) {
	switch t := v.(type) {
	case *jsonObject:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(indent + "  ")
			writeString(b, k)
			b.WriteString(": ")
			writeValue(b, t.vals[k], indent+"  ")
			if i < len(t.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(indent + "  ")
			writeValue(b, e, indent+"  ")
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case string:
		writeString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case int:
		b.WriteString(strconv.Itoa(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case nil:
		b.WriteString("null")
	default:
		panic(fmt.Sprintf("jsonObject: unsupported value %T", v))
	}
}

func writeString(b *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Write(bytes.TrimSuffix(tmp.Bytes(), []byte("\n")))
}

// stripJSONComments removes // and /* */ comments outside strings (Gemini
// CLI's settings.json allows them) and reports whether there were any.
func stripJSONComments(b []byte) ([]byte, bool) {
	var out bytes.Buffer
	found := false
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out.WriteByte(c)
			switch c {
			case '\\':
				if i+1 < len(b) {
					i++
					out.WriteByte(b[i])
				}
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			found = true
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out.WriteByte('\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			found = true
			end := bytes.Index(b[i+2:], []byte("*/"))
			if end < 0 {
				i = len(b)
				break
			}
			// Keep line breaks so parse errors point at the right line.
			out.WriteString(strings.Repeat("\n", bytes.Count(b[i:i+2+end], []byte("\n"))))
			i += end + 3
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes(), found
}
