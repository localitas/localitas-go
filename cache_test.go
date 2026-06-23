package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupCacheServer(t *testing.T) (*httptest.Server, *Client) {
	t.Helper()
	store := make(map[string]string)

	mux := http.NewServeMux()

	mux.HandleFunc("POST /apps/cache/api/caches", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"name":"test","created_at":1}`))
	})
	mux.HandleFunc("GET /apps/cache/api/caches", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"caches": []map[string]interface{}{
				{"name": "default", "key_count": 0, "hit_rate": 0},
			},
		})
	})
	mux.HandleFunc("DELETE /apps/cache/api/caches/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"deleted"}`))
	})
	mux.HandleFunc("PUT /apps/cache/api/caches/{name}/keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		var req struct {
			Value string `json:"value"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		store[key] = req.Value
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /apps/cache/api/caches/{name}/keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		val, ok := store[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": key, "value": val, "ttl_remaining": 300,
		})
	})
	mux.HandleFunc("DELETE /apps/cache/api/caches/{name}/keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
		delete(store, r.PathValue("key"))
		w.Write([]byte(`{"status":"deleted"}`))
	})
	mux.HandleFunc("GET /apps/cache/api/caches/{name}/keys", func(w http.ResponseWriter, r *http.Request) {
		var keys []string
		for k := range store {
			keys = append(keys, k)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})
	})
	mux.HandleFunc("POST /apps/cache/api/caches/{name}/flush", func(w http.ResponseWriter, r *http.Request) {
		for k := range store {
			delete(store, k)
		}
		w.Write([]byte(`{"status":"flushed"}`))
	})
	mux.HandleFunc("GET /apps/cache/api/caches/{name}/stats", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(CacheStats{Name: "test", Hits: 10, Misses: 2, KeyCount: 5, HitRate: 83.3})
	})
	mux.HandleFunc("POST /apps/cache/api/caches/{name}/incr/{key...}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"key": r.PathValue("key"), "value": 1})
	})
	mux.HandleFunc("POST /apps/cache/api/caches/{name}/expire/{key...}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL).WithToken("test-token")
}

func TestCacheRef_SetAndGet(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")
	ctx := context.Background()

	if err := cache.Set(ctx, "k1", "v1", 5*time.Minute); err != nil {
		t.Fatal(err)
	}

	val, err := cache.Get(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if val != "v1" {
		t.Errorf("value = %q, want v1", val)
	}
}

func TestCacheRef_GetMiss(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")

	_, err := cache.Get(context.Background(), "missing")
	if err == nil {
		t.Error("expected error for missing key")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
}

func TestCacheRef_Del(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")
	ctx := context.Background()

	cache.Set(ctx, "k", "v", 0)
	if err := cache.Del(ctx, "k"); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRef_Incr(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")

	val, err := cache.Incr(context.Background(), "counter")
	if err != nil {
		t.Fatal(err)
	}
	if val != 1 {
		t.Errorf("incr = %d, want 1", val)
	}
}

func TestCacheRef_Keys(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")
	ctx := context.Background()

	cache.Set(ctx, "a", "1", 0)
	cache.Set(ctx, "b", "2", 0)

	keys, err := cache.Keys(ctx, "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("keys = %d, want 2", len(keys))
	}
}

func TestCacheRef_Flush(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")
	ctx := context.Background()

	cache.Set(ctx, "a", "1", 0)
	if err := cache.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	keys, _ := cache.Keys(ctx, "*")
	if len(keys) != 0 {
		t.Errorf("keys after flush = %d, want 0", len(keys))
	}
}

func TestCacheRef_Stats(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")

	stats, err := cache.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 10 {
		t.Errorf("hits = %d, want 10", stats.Hits)
	}
	if stats.HitRate < 83 {
		t.Errorf("hit_rate = %.1f, want ~83.3", stats.HitRate)
	}
}

func TestCacheRef_Expire(t *testing.T) {
	_, c := setupCacheServer(t)
	cache := c.Cache("test")
	ctx := context.Background()

	cache.Set(ctx, "k", "v", 0)
	if err := cache.Expire(ctx, "k", 60*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestClient_CreateCache(t *testing.T) {
	_, c := setupCacheServer(t)
	if err := c.CreateCache(context.Background(), "new-cache"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ListCaches(t *testing.T) {
	_, c := setupCacheServer(t)
	caches, err := c.ListCaches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(caches) != 1 {
		t.Errorf("caches = %d, want 1", len(caches))
	}
	if caches[0].Name != "default" {
		t.Errorf("name = %q, want default", caches[0].Name)
	}
}

func TestClient_DeleteCache(t *testing.T) {
	_, c := setupCacheServer(t)
	if err := c.DeleteCache(context.Background(), "temp"); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRef_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("my-secret-token")
	cache := c.Cache("test")
	cache.Flush(context.Background())

	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("expected Bearer token, got %q", gotAuth)
	}
	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("auth = %q, want 'Bearer my-secret-token'", gotAuth)
	}
}
