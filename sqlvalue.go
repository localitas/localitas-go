package client

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// SQLKind is which SQL scalar a SQLValue holds.
type SQLKind uint8

const (
	SQLKindNull SQLKind = iota
	SQLKindInt
	SQLKindReal
	SQLKindText
	SQLKindBlob
	SQLKindBool
	SQLKindTime
)

// SQLValue is one SQL scalar as a column holds it and the data API carries
// it: NULL, an integer, a real, text, a blob, a boolean or a time. Its zero
// value is NULL.
//
// On the wire it is JSON null, a number, a string or a boolean: a blob is the
// text it holds and a time is RFC 3339 text. A JSON number decodes as an
// integer when it is one in range, otherwise as a real, and as text when no
// float64 holds it, so integers above 2^53 keep every digit. It binds as a
// statement argument (driver.Valuer) and scans from a column (sql.Scanner).
type SQLValue struct {
	kind SQLKind
	i    int64
	f    float64
	s    string
	t    time.Time
}

func SQLNull() SQLValue            { return SQLValue{} }
func SQLInt(i int64) SQLValue      { return SQLValue{kind: SQLKindInt, i: i} }
func SQLReal(f float64) SQLValue   { return SQLValue{kind: SQLKindReal, f: f} }
func SQLText(s string) SQLValue    { return SQLValue{kind: SQLKindText, s: s} }
func SQLBlob(b []byte) SQLValue    { return SQLValue{kind: SQLKindBlob, s: string(b)} }
func SQLTime(t time.Time) SQLValue { return SQLValue{kind: SQLKindTime, t: t} }

func SQLBool(b bool) SQLValue {
	v := SQLValue{kind: SQLKindBool}
	if b {
		v.i = 1
	}
	return v
}

func (v SQLValue) Kind() SQLKind { return v.kind }

func (v SQLValue) IsNull() bool { return v.kind == SQLKindNull }

// Int64 is the value when it is an integer.
func (v SQLValue) Int64() (int64, bool) { return v.i, v.kind == SQLKindInt }

// Float64 is the value when it is a real or an integer.
func (v SQLValue) Float64() (float64, bool) {
	switch v.kind {
	case SQLKindReal:
		return v.f, true
	case SQLKindInt:
		return float64(v.i), true
	}
	return 0, false
}

// Text is the value when it is text or a blob.
func (v SQLValue) Text() (string, bool) {
	return v.s, v.kind == SQLKindText || v.kind == SQLKindBlob
}

// Bool is the value when it is a boolean.
func (v SQLValue) Bool() (bool, bool) { return v.i != 0, v.kind == SQLKindBool }

// Time is the value when it is a time.
func (v SQLValue) Time() (time.Time, bool) { return v.t, v.kind == SQLKindTime }

// String is the value as text: empty for NULL, numbers as Go prints them.
func (v SQLValue) String() string {
	switch v.kind {
	case SQLKindInt:
		return strconv.FormatInt(v.i, 10)
	case SQLKindReal:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case SQLKindText, SQLKindBlob:
		return v.s
	case SQLKindBool:
		return strconv.FormatBool(v.i != 0)
	case SQLKindTime:
		return v.t.String()
	}
	return ""
}

// MarshalJSON writes text unescaped: the encoder escapes HTML in it as it
// escapes any string, so the value is written as the scalar itself would be.
func (v SQLValue) MarshalJSON() ([]byte, error) {
	switch v.kind {
	case SQLKindInt:
		return strconv.AppendInt(nil, v.i, 10), nil
	case SQLKindReal:
		return json.Marshal(v.f)
	case SQLKindText, SQLKindBlob:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v.s); err != nil {
			return nil, err
		}
		return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
	case SQLKindBool:
		return strconv.AppendBool(nil, v.i != 0), nil
	case SQLKindTime:
		return v.t.MarshalJSON()
	}
	return []byte("null"), nil
}

func (v *SQLValue) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
		return fmt.Errorf("an empty SQL value")
	case string(b) == "null":
		*v = SQLNull()
	case string(b) == "true":
		*v = SQLBool(true)
	case string(b) == "false":
		*v = SQLBool(false)
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*v = SQLText(s)
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		n := json.Number(b)
		if i, err := n.Int64(); err == nil {
			*v = SQLInt(i)
		} else if f, err := n.Float64(); err == nil {
			*v = SQLReal(f)
		} else {
			*v = SQLText(n.String())
		}
	default:
		return fmt.Errorf("a SQL value is null, a number, a string or a boolean, not %s", b)
	}
	return nil
}

// MarshalYAML is the scalar itself, a blob as its text.
func (v SQLValue) MarshalYAML() (any, error) {
	switch v.kind {
	case SQLKindInt:
		return v.i, nil
	case SQLKindReal:
		return v.f, nil
	case SQLKindText, SQLKindBlob:
		return v.s, nil
	case SQLKindBool:
		return v.i != 0, nil
	case SQLKindTime:
		return v.t, nil
	}
	return nil, nil
}

// Scan takes a column's value as a database/sql driver gives it.
func (v *SQLValue) Scan(src any) error {
	switch x := src.(type) {
	case nil:
		*v = SQLNull()
	case int64:
		*v = SQLInt(x)
	case float64:
		*v = SQLReal(x)
	case bool:
		*v = SQLBool(x)
	case []byte:
		*v = SQLBlob(x)
	case string:
		*v = SQLText(x)
	case time.Time:
		*v = SQLTime(x)
	default:
		return fmt.Errorf("a SQL value cannot be a %T", src)
	}
	return nil
}

// Value binds the value as the driver value of its kind.
func (v SQLValue) Value() (driver.Value, error) {
	switch v.kind {
	case SQLKindInt:
		return v.i, nil
	case SQLKindReal:
		return v.f, nil
	case SQLKindText:
		return v.s, nil
	case SQLKindBlob:
		return []byte(v.s), nil
	case SQLKindBool:
		return v.i != 0, nil
	case SQLKindTime:
		return v.t, nil
	}
	return nil, nil
}
