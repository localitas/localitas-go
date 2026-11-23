package client

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// APISpec is an app's description of its HTTP API, served at GET
// /swagger.json: core's AI tool index reads it and makes each endpoint a
// tool the model may call. Core's own apps serve the same shape.
type APISpec struct {
	AppName     string   `json:"app_name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords,omitempty"`
	// Fields document conventions the endpoints share, such as the date
	// formats they take.
	Fields    []APIField    `json:"fields,omitempty"`
	Endpoints []APIEndpoint `json:"endpoints"`
}

// APIField documents a convention an app's endpoints share.
type APIField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

// APIEndpoint is one endpoint of an app's API.
type APIEndpoint struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	// Formats are the response formats the endpoint can answer in; none
	// means markdown, yaml and json.
	Formats     []string   `json:"formats,omitempty"`
	QueryParams []APIParam `json:"query_params,omitempty"`
	RequestBody *APIBody   `json:"request_body,omitempty"`
	Response    *APIBody   `json:"response,omitempty"`
}

// APIParam is a query parameter of an endpoint.
type APIParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// UnmarshalJSON reads a parameter as an object, or as its bare name: a
// string parameter, optional and undescribed.
func (p *APIParam) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*p = APIParam{Name: name, Type: "string"}
		return nil
	}
	type param APIParam
	var v param
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = APIParam(v)
	return nil
}

// APIBody is a request or response body: its content type and an example
// of it, written out as the body would be.
type APIBody struct {
	ContentType string `json:"content_type"`
	Example     string `json:"example"`
}

// JSONExample is a JSON body whose example is example, a JSON document.
func JSONExample(example string) *APIBody {
	return &APIBody{ContentType: "application/json", Example: example}
}

// JSONExampleOf is a JSON body whose example is v encoded: an example of
// the request type its handler reads is never shaped apart from it. It
// panics on a value that cannot encode: a programming error, caught when the
// spec is built.
func JSONExampleOf(v any) *APIBody {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("api example %T: %v", v, err))
	}
	return JSONExample(string(b))
}

// ServeHTTP serves the spec as the app's swagger.json.
func (s APISpec) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// StringParams are optional, undescribed string query parameters, by name.
func StringParams(names ...string) []APIParam {
	params := make([]APIParam, len(names))
	for i, name := range names {
		params[i] = APIParam{Name: name, Type: "string"}
	}
	return params
}
