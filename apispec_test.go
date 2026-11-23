package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// The JSON an app serves at /swagger.json is the shape core's tool index has
// always read: every key, in this order, with these encodings.
func TestAPISpec_WireJSON(t *testing.T) {
	spec := APISpec{
		AppName:     "Calendar",
		Version:     "0.1.0",
		Description: "Events",
		Keywords:    []string{"calendar"},
		Fields:      []APIField{{Name: "Dates", Description: "Formats", Example: "2026-04-23"}},
		Endpoints: []APIEndpoint{
			{
				Method:      "GET",
				Path:        "/api/events",
				Summary:     "List events",
				QueryParams: []APIParam{{Name: "from", Type: "string", Required: true, Description: "Start"}},
				Response:    JSONExample(`{"events":[]}`),
			},
			{
				Method:      "POST",
				Path:        "/api/events",
				Summary:     "Create an event",
				Description: "Creates it",
				Formats:     []string{"json"},
				RequestBody: JSONExampleOf(struct {
					Title string `json:"title"`
				}{Title: "Lunch"}),
			},
		},
	}
	got, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"app_name":"Calendar","version":"0.1.0","description":"Events","keywords":["calendar"],` +
		`"fields":[{"name":"Dates","description":"Formats","example":"2026-04-23"}],"endpoints":[` +
		`{"method":"GET","path":"/api/events","summary":"List events","query_params":[{"name":"from","type":"string","required":true,"description":"Start"}],"response":{"content_type":"application/json","example":"{\"events\":[]}"}},` +
		`{"method":"POST","path":"/api/events","summary":"Create an event","description":"Creates it","formats":["json"],"request_body":{"content_type":"application/json","example":"{\"title\":\"Lunch\"}"}}]}`
	if string(got) != want {
		t.Fatalf("spec JSON\n got %s\nwant %s", got, want)
	}

	var back APISpec
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Fatalf("spec read back writes\n got %s\nwant %s", again, want)
	}
}

// An app serves its spec as JSON, the spec's own encoding.
func TestAPISpec_ServesItselfAsSwaggerJSON(t *testing.T) {
	spec := APISpec{AppName: "Notes", Version: "1", Description: "d", Endpoints: []APIEndpoint{{Method: "GET", Path: "/api/notes", Summary: "List"}}}
	rec := httptest.NewRecorder()
	spec.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger.json", nil))
	want, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(rec.Body.String()) != string(want) {
		t.Fatalf("served %d %q %s, want 200 application/json %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String(), want)
	}
}

// A spec without fields, descriptions or formats leaves their keys out.
func TestAPISpec_WireJSONLeavesOutWhatIsUnset(t *testing.T) {
	got, err := json.Marshal(APISpec{AppName: "Search", Version: "1", Description: "d", Endpoints: []APIEndpoint{{Method: "GET", Path: "/api/q", Summary: "s"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"app_name":"Search","version":"1","description":"d","endpoints":[{"method":"GET","path":"/api/q","summary":"s"}]}`
	if string(got) != want {
		t.Fatalf("spec JSON\n got %s\nwant %s", got, want)
	}
}

// A query parameter given by its bare name is an optional string parameter.
func TestAPIParam_ReadsABareName(t *testing.T) {
	var ep APIEndpoint
	if err := json.Unmarshal([]byte(`{"method":"GET","path":"/api/notes","summary":"List","query_params":["limit",{"name":"q","type":"string","required":true,"description":"Query"}]}`), &ep); err != nil {
		t.Fatal(err)
	}
	want := []APIParam{{Name: "limit", Type: "string"}, {Name: "q", Type: "string", Required: true, Description: "Query"}}
	if !slices.Equal(ep.QueryParams, want) {
		t.Fatalf("query params %+v, want %+v", ep.QueryParams, want)
	}
}

// StringParams names optional string parameters.
func TestStringParams(t *testing.T) {
	want := []APIParam{{Name: "q", Type: "string"}, {Name: "limit", Type: "string"}}
	if got := StringParams("q", "limit"); !slices.Equal(got, want) {
		t.Fatalf("StringParams gave %+v, want %+v", got, want)
	}
}
