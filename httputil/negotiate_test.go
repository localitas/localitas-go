package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNegotiateFormat_DefaultJSON(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)
	if got := NegotiateFormat(r); got != FormatJSON {
		t.Fatalf("expected json, got %s", got)
	}
}

func TestNegotiateFormat_AcceptYAML(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "application/yaml")
	if got := NegotiateFormat(r); got != FormatYAML {
		t.Fatalf("expected yaml, got %s", got)
	}
}

func TestNegotiateFormat_AcceptMarkdown(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "text/markdown")
	if got := NegotiateFormat(r); got != FormatMarkdown {
		t.Fatalf("expected markdown, got %s", got)
	}
}

func TestNegotiateFormat_LLMCaller_DefaultMarkdown(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Caller", "llm")
	if got := NegotiateFormat(r); got != FormatMarkdown {
		t.Fatalf("expected markdown for LLM caller, got %s", got)
	}
}

func TestNegotiateFormat_LLMCaller_AcceptYAML(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Caller", "llm")
	r.Header.Set("Accept", "application/yaml")
	if got := NegotiateFormat(r); got != FormatYAML {
		t.Fatalf("expected yaml when LLM explicitly requests yaml, got %s", got)
	}
}

func TestNegotiateFormat_LLMCaller_CaseInsensitive(t *testing.T) {
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Caller", "LLM")
	if got := NegotiateFormat(r); got != FormatMarkdown {
		t.Fatalf("expected markdown for uppercase LLM, got %s", got)
	}
}

func TestWriteResponse_JSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	data := map[string]string{"name": "test"}
	WriteResponse(w, r, http.StatusOK, data)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}

	var parsed map[string]string
	if err := json.NewDecoder(w.Body).Decode(&parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["name"] != "test" {
		t.Fatalf("expected name=test, got %s", parsed["name"])
	}
}

func TestWriteResponse_YAML(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "application/yaml")

	data := map[string]string{"name": "test"}
	WriteResponse(w, r, http.StatusOK, data)

	if ct := w.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Fatalf("expected application/yaml, got %s", ct)
	}

	var parsed map[string]string
	if err := yaml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if parsed["name"] != "test" {
		t.Fatalf("expected name=test, got %s", parsed["name"])
	}
}

func TestWriteResponse_Markdown_List(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Caller", "llm")

	type Item struct {
		Name   string `json:"name"`
		Method string `json:"method"`
	}
	data := []Item{
		{Name: "getUser", Method: "GET"},
		{Name: "createUser", Method: "POST"},
	}
	WriteResponse(w, r, http.StatusOK, data)

	if ct := w.Header().Get("Content-Type"); ct != "text/markdown" {
		t.Fatalf("expected text/markdown, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "| method") {
		t.Fatalf("expected markdown table, got:\n%s", body)
	}
	if !strings.Contains(body, "getUser") {
		t.Fatalf("expected getUser in table, got:\n%s", body)
	}
}

func TestWriteResponse_Markdown_SingleObject(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Accept", "text/markdown")

	type Detail struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	data := Detail{Name: "getUser", Path: "/users/{id}"}
	WriteResponse(w, r, http.StatusOK, data)

	body := w.Body.String()
	if !strings.Contains(body, "| field | value |") {
		t.Fatalf("expected KV table, got:\n%s", body)
	}
	if !strings.Contains(body, "| name | getUser |") {
		t.Fatalf("expected name row, got:\n%s", body)
	}
}

func TestWriteError_JSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	WriteError(w, r, http.StatusBadRequest, "bad %s", "input")

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var parsed map[string]string
	if err := json.NewDecoder(w.Body).Decode(&parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["error"] != "bad input" {
		t.Fatalf("expected 'bad input', got %s", parsed["error"])
	}
}

func TestRenderMarkdown_NestedDotNotation(t *testing.T) {
	type Inner struct {
		Port int `json:"port"`
	}
	type Outer struct {
		Name   string `json:"name"`
		Server Inner  `json:"server"`
	}

	md := RenderMarkdown(Outer{Name: "api", Server: Inner{Port: 8080}})
	if !strings.Contains(md, "server.port") {
		t.Fatalf("expected dot notation, got:\n%s", md)
	}
}

func TestRenderMarkdown_EmptyList(t *testing.T) {
	md := RenderMarkdown([]string{})
	if md != "(empty)\n" {
		t.Fatalf("expected (empty), got: %s", md)
	}
}

func TestRenderMarkdown_Nil(t *testing.T) {
	md := RenderMarkdown(nil)
	if md != "" {
		t.Fatalf("expected empty, got: %s", md)
	}
}
