package httputil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	FormatJSON     = "json"
	FormatYAML     = "yaml"
	FormatMarkdown = "markdown"
)

func NegotiateFormat(r *http.Request) string {
	caller := r.Header.Get("Caller")
	accept := r.Header.Get("Accept")

	if strings.EqualFold(caller, "llm") {
		if strings.Contains(accept, "text/markdown") {
			return FormatMarkdown
		}
		if strings.Contains(accept, "application/yaml") || strings.Contains(accept, "text/yaml") {
			return FormatYAML
		}
		return FormatMarkdown
	}

	if strings.Contains(accept, "text/markdown") {
		return FormatMarkdown
	}
	if strings.Contains(accept, "application/yaml") || strings.Contains(accept, "text/yaml") {
		return FormatYAML
	}

	return FormatJSON
}

func WriteResponse(w http.ResponseWriter, r *http.Request, status int, data interface{}) {
	format := NegotiateFormat(r)

	switch format {
	case FormatMarkdown:
		writeMarkdown(w, status, data)
	case FormatYAML:
		writeYAML(w, status, data)
	default:
		WriteJSON(w, status, data)
	}
}

func WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	WriteResponse(w, r, status, map[string]string{"error": msg})
}

func writeYAML(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(status)

	yamlBytes, err := yaml.Marshal(data)
	if err != nil {
		return
	}
	w.Write(yamlBytes)
}

func writeMarkdown(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "text/markdown")
	w.WriteHeader(status)
	w.Write([]byte(RenderMarkdown(data)))
}

func RenderMarkdown(data interface{}) string {
	if data == nil {
		return ""
	}

	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}

	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		return renderListAsTable(v)
	}

	return renderObjectAsKV(data)
}

func renderListAsTable(v reflect.Value) string {
	if v.Len() == 0 {
		return "(empty)\n"
	}

	var rows []map[string]string
	for i := 0; i < v.Len(); i++ {
		flat := flattenValue("", v.Index(i))
		rows = append(rows, flat)
	}

	colSet := map[string]bool{}
	for _, row := range rows {
		for k := range row {
			colSet[k] = true
		}
	}
	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	var buf strings.Builder
	buf.WriteString("| ")
	for i, c := range cols {
		if i > 0 {
			buf.WriteString(" | ")
		}
		buf.WriteString(c)
	}
	buf.WriteString(" |\n")

	buf.WriteString("|")
	for range cols {
		buf.WriteString("---|")
	}
	buf.WriteString("\n")

	for _, row := range rows {
		buf.WriteString("| ")
		for i, c := range cols {
			if i > 0 {
				buf.WriteString(" | ")
			}
			val := row[c]
			val = strings.ReplaceAll(val, "|", "\\|")
			val = strings.ReplaceAll(val, "\n", " ")
			buf.WriteString(val)
		}
		buf.WriteString(" |\n")
	}

	return buf.String()
}

func renderObjectAsKV(data interface{}) string {
	flat := flattenValue("", reflect.ValueOf(data))
	if len(flat) == 0 {
		return "(empty)\n"
	}

	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	buf.WriteString("| field | value |\n")
	buf.WriteString("|---|---|\n")

	for _, k := range keys {
		val := strings.ReplaceAll(flat[k], "|", "\\|")
		val = strings.ReplaceAll(val, "\n", " ")
		buf.WriteString("| ")
		buf.WriteString(k)
		buf.WriteString(" | ")
		buf.WriteString(val)
		buf.WriteString(" |\n")
	}

	return buf.String()
}

func flattenValue(prefix string, v reflect.Value) map[string]string {
	result := make(map[string]string)

	if v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			if prefix != "" {
				result[prefix] = ""
			}
			return result
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name := fieldName(field)
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			sub := flattenValue(key, v.Field(i))
			for k, val := range sub {
				result[k] = val
			}
		}

	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			name := fmt.Sprintf("%v", iter.Key().Interface())
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			sub := flattenValue(key, iter.Value())
			for k, val := range sub {
				result[k] = val
			}
		}

	case reflect.Slice, reflect.Array:
		if v.Len() == 0 {
			if prefix != "" {
				result[prefix] = "[]"
			}
		} else if isScalarSlice(v) {
			parts := make([]string, v.Len())
			for i := 0; i < v.Len(); i++ {
				parts[i] = fmt.Sprintf("%v", v.Index(i).Interface())
			}
			key := prefix
			if key == "" {
				key = "value"
			}
			result[key] = strings.Join(parts, ", ")
		} else {
			jsonBytes, err := json.Marshal(v.Interface())
			if err == nil {
				key := prefix
				if key == "" {
					key = "value"
				}
				result[key] = string(jsonBytes)
			}
		}

	default:
		key := prefix
		if key == "" {
			key = "value"
		}
		result[key] = fmt.Sprintf("%v", v.Interface())
	}

	return result
}

func isScalarSlice(v reflect.Value) bool {
	if v.Len() == 0 {
		return true
	}
	elem := v.Index(0)
	if elem.Kind() == reflect.Interface {
		elem = elem.Elem()
	}
	switch elem.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Bool:
		return true
	}
	return false
}

func fieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		tag = f.Tag.Get("yaml")
	}
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" && parts[0] != "-" {
			return parts[0]
		}
	}
	return f.Name
}
