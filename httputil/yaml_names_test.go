package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type yamlAnswerItem struct {
	Name string `json:"name"`
}

type yamlAnswer struct {
	GroupIDs []string         `json:"group_ids"`
	ZipCode  string           `json:"zip_code"`
	Enabled  string           `json:"enabled"`
	Missing  string           `json:"missing"`
	Big      json.Number      `json:"big"`
	Note     string           `json:"note"`
	Items    []yamlAnswerItem `json:"items"`
	Empty    []string         `json:"empty"`
	Skipped  string           `json:"skipped,omitempty"`
}

// A YAML answer names its fields as the JSON answer does, in the JSON's
// order, as block YAML: a typed answer's fields came out under yaml.v3's
// lowercased Go names (group_ids as groupids), so a caller asking for YAML
// got other keys than one asking for JSON. A string that reads as a number,
// a bool or null stays a string; a large integer keeps its digits.
func TestWriteResponse_YAMLNamesFieldsAsJSONDoes(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Accept", "application/yaml")
	w := httptest.NewRecorder()
	WriteResponse(w, r, http.StatusOK, yamlAnswer{
		GroupIDs: []string{"g1", "g2"},
		ZipCode:  "02139",
		Enabled:  "true",
		Missing:  "null",
		Big:      json.Number("12345678901234567890"),
		Note:     "a: b",
		Items:    []yamlAnswerItem{{Name: "one"}},
		Empty:    []string{},
	})
	want := `group_ids:
    - g1
    - g2
zip_code: "02139"
enabled: "true"
missing: "null"
big: 12345678901234567890
note: 'a: b'
items:
    - name: one
empty: []
`
	if got := w.Body.String(); got != want {
		t.Fatalf("YAML answer:\n%s\nwant:\n%s", got, want)
	}
}
