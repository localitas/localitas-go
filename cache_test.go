package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// cacheFake serves core's cache routes (the patterns of core's
// apps/cache RegisterRoutes) with canned bodies in core's wire shape:
// data endpoints answer {"error":"","result":...}, an unknown cache is a
// plain-text 404, and CreateCache/DeleteCache answer unwrapped JSON.
type cacheFake struct {
	mux    *http.ServeMux
	client *Client
	mu     sync.Mutex
	calls  []cacheCall
}

type cacheCall struct {
	Method  string
	Pattern string
	Key     string
	Member  string
	Field   string
	Group   string
	Query   string
	Body    string
}

func newCacheFake(t *testing.T) *cacheFake {
	t.Helper()
	f := &cacheFake{mux: http.NewServeMux()}
	srv := httptest.NewServer(f.mux)
	t.Cleanup(srv.Close)
	f.client = New(srv.URL).WithToken("test-token")
	return f
}

func (f *cacheFake) reply(pattern string, status int, body string) {
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, cacheCall{
			Method:  r.Method,
			Pattern: r.Pattern,
			Key:     r.PathValue("key"),
			Member:  r.PathValue("member"),
			Field:   r.PathValue("field"),
			Group:   r.PathValue("group"),
			Query:   r.URL.RawQuery,
			Body:    string(raw),
		})
		f.mu.Unlock()
		if strings.HasPrefix(body, "{") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.WriteHeader(status)
		io.WriteString(w, body+"\n")
	})
}

func (f *cacheFake) last(t *testing.T) cacheCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no request reached the fake")
	}
	return f.calls[len(f.calls)-1]
}

func (f *cacheFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func okResult(result string) string { return `{"error":"","result":` + result + `}` }

func errResult(msg string) string { return `{"error":"` + msg + `","result":null}` }

const unknownCacheBody = `cache "test" not found`

func requireAPIStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError with status %d, got %T: %v", status, err, err)
	}
	if apiErr.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %s)", apiErr.StatusCode, status, apiErr.Body)
	}
}

func requireCall(t *testing.T, got cacheCall, method, pattern, body string) {
	t.Helper()
	if got.Method != method || got.Pattern != pattern || got.Body != body {
		t.Fatalf("request = %s %q body %s, want %s %q body %s", got.Method, got.Pattern, got.Body, method, pattern, body)
	}
}

func TestCacheRef_KeyValueDecodesCoreEnvelope(t *testing.T) {
	f := newCacheFake(t)
	f.reply("GET /apps/cache/api/caches/{name}/keys/{key...}", 200, okResult(`{"key":"k1","ttl_remaining":300,"value":"v1"}`))
	f.reply("PUT /apps/cache/api/caches/{name}/keys/{key...}", 201, okResult(`{"status":"ok"}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/keys/{key...}", 200, okResult(`{"status":"deleted"}`))
	f.reply("GET /apps/cache/api/caches/{name}/keys", 200, okResult(`{"keys":["user:1","user:2"]}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/incr/{key...}", 200, okResult(`{"key":"counter","value":4}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/incrttl/{key...}", 200, okResult(`{"key":"rate","value":3}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/expire/{key...}", 200, okResult(`{"status":"ok"}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/setnx/{key...}", 200, okResult(`{"acquired":true,"key":"lock"}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/getset/{key...}", 200, okResult(`{"had_old":true,"key":"lock","old_value":"owner-a"}`))
	f.reply("POST /apps/cache/api/caches/{name}/flush", 200, okResult(`{"status":"flushed"}`))
	f.reply("GET /apps/cache/api/caches/{name}/stats", 200, okResult(`{"name":"test","hits":1,"misses":1,"sets":3,"deletes":0,"evictions":0,"key_count":3,"hit_rate":50}`))
	c := f.client.Cache("test")
	ctx := context.Background()

	got, err := c.Get(ctx, "k1")
	if err != nil || got != "v1" {
		t.Fatalf("Get = %q, %v; want v1", got, err)
	}
	if err := c.Set(ctx, "k1", "v1", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "PUT", "PUT /apps/cache/api/caches/{name}/keys/{key...}", `{"value":"v1","ttl":300}`)
	if err := c.Del(ctx, "k1"); err != nil {
		t.Fatal(err)
	}

	keys, err := c.Keys(ctx, "user:*")
	if err != nil || !slices.Equal(keys, []string{"user:1", "user:2"}) {
		t.Fatalf("Keys = %q, %v", keys, err)
	}
	if q := f.last(t).Query; q != "pattern=user%3A%2A" {
		t.Fatalf("Keys query = %q", q)
	}

	v, err := c.IncrBy(ctx, "counter", 0)
	if err != nil || v != 4 {
		t.Fatalf("IncrBy = %d, %v; want 4", v, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/keys/incr/{key...}", `{"delta":0}`)
	if _, err := c.Incr(ctx, "counter"); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/keys/incr/{key...}", `{"delta":1}`)

	v, err = c.IncrWithTTL(ctx, "rate", 2, time.Minute)
	if err != nil || v != 3 {
		t.Fatalf("IncrWithTTL = %d, %v; want 3", v, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/keys/incrttl/{key...}", `{"delta":2,"ttl":60}`)

	if err := c.Expire(ctx, "k1", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/keys/expire/{key...}", `{"ttl":30}`)

	acquired, err := c.SetNX(ctx, "lock", "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("SetNX = %v, %v; want acquired", acquired, err)
	}
	old, had, err := c.GetSet(ctx, "lock", "owner-c", 0)
	if err != nil || !had || old != "owner-a" {
		t.Fatalf("GetSet = %q, %v, %v; want owner-a, true", old, had, err)
	}

	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := c.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (CacheStats{Name: "test", Hits: 1, Misses: 1, Sets: 3, KeyCount: 3, HitRate: 50}); *stats != want {
		t.Fatalf("Stats = %+v, want %+v", *stats, want)
	}
}

func TestCacheRef_TTL(t *testing.T) {
	const route = "GET /apps/cache/api/caches/{name}/keys/{key...}"
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		body string
		want time.Duration
	}{
		{"no expiry", okResult(`{"key":"k","ttl_remaining":-1,"value":"v"}`), -time.Second},
		{"remaining", okResult(`{"key":"k","ttl_remaining":42,"value":"v"}`), 42 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCacheFake(t)
			f.reply(route, 200, tc.body)
			got, err := f.client.Cache("test").TTL(ctx, "k")
			if err != nil || got != tc.want {
				t.Fatalf("TTL = %v, %v; want %v", got, err, tc.want)
			}
			if n := f.callCount(); n != 1 {
				t.Fatalf("TTL made %d requests, want 1", n)
			}
		})
	}

	t.Run("absent key", func(t *testing.T) {
		f := newCacheFake(t)
		f.reply(route, 404, errResult("key not found"))
		got, err := f.client.Cache("test").TTL(ctx, "absent")
		if err != nil || got != -2*time.Second {
			t.Fatalf("TTL = %v, %v; want -2s", got, err)
		}
	})

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unknown cache", 404, unknownCacheBody},
		{"server error", 500, errResult("database is locked")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCacheFake(t)
			f.reply(route, tc.status, tc.body)
			_, err := f.client.Cache("test").TTL(ctx, "k")
			requireAPIStatus(t, err, tc.status)
		})
	}
}

func TestCacheRef_KeysAreEscaped(t *testing.T) {
	f := newCacheFake(t)
	f.reply("GET /apps/cache/api/caches/{name}/keys/{key...}", 200, okResult(`{"key":"k","ttl_remaining":-1,"value":"v"}`))
	f.reply("PUT /apps/cache/api/caches/{name}/keys/{key...}", 201, okResult(`{"status":"ok"}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/keys/{key...}", 200, okResult(`{"status":"deleted"}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/incr/{key...}", 200, okResult(`{"key":"k","value":1}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/incrttl/{key...}", 200, okResult(`{"key":"k","value":1}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/expire/{key...}", 200, okResult(`{"status":"ok"}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/setnx/{key...}", 200, okResult(`{"acquired":true,"key":"k"}`))
	f.reply("POST /apps/cache/api/caches/{name}/keys/getset/{key...}", 200, okResult(`{"had_old":false,"key":"k","old_value":""}`))
	f.reply("GET /apps/cache/api/caches/{name}/zsets/{zset}/score/{member}", 200, okResult(`{"score":1}`))
	f.reply("GET /apps/cache/api/caches/{name}/hashes/{hash}/fields/{field}", 200, okResult(`{"value":"v"}`))
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/claim", 200, okResult(`{"message":null}`))
	c := f.client.Cache("test")
	ctx := context.Background()

	for _, key := range []string{"q?x=1#frag", "a/b/c", "sp ace%", "../up"} {
		calls := []struct {
			name string
			call func() error
		}{
			{"Get", func() error { _, err := c.Get(ctx, key); return err }},
			{"TTL", func() error { _, err := c.TTL(ctx, key); return err }},
			{"Set", func() error { return c.Set(ctx, key, "v", 0) }},
			{"Del", func() error { return c.Del(ctx, key) }},
			{"IncrBy", func() error { _, err := c.IncrBy(ctx, key, 1); return err }},
			{"IncrWithTTL", func() error { _, err := c.IncrWithTTL(ctx, key, 1, time.Minute); return err }},
			{"Expire", func() error { return c.Expire(ctx, key, time.Minute) }},
			{"SetNX", func() error { _, err := c.SetNX(ctx, key, "v", 0); return err }},
			{"GetSet", func() error { _, _, err := c.GetSet(ctx, key, "v", 0); return err }},
		}
		for _, tc := range calls {
			if err := tc.call(); err != nil {
				t.Fatalf("%s(%q): %v", tc.name, key, err)
			}
			if got := f.last(t).Key; got != key {
				t.Fatalf("%s(%q): core received key %q", tc.name, key, got)
			}
		}
	}

	member := "a/b?c"
	if _, err := c.SortedSet("board").Score(ctx, member); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).Member; got != member {
		t.Fatalf("Score member = %q, want %q", got, member)
	}
	field := "x#y"
	if _, err := c.Hash("h").Get(ctx, field); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).Field; got != field {
		t.Fatalf("hash field = %q, want %q", got, field)
	}
	if _, err := c.PubSub("ch", PubSubOpts{}).Claim(ctx, "g?1", "w&1"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t); got.Group != "g?1" || got.Query != "consumer=w%261" {
		t.Fatalf("claim group %q query %q", got.Group, got.Query)
	}
}

func TestCacheRef_FailuresAreAPIErrors(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		pattern string
		status  int
		body    string
		call    func(*CacheRef) error
	}{
		{"Get absent key", "GET /apps/cache/api/caches/{name}/keys/{key...}", 404, errResult("key not found"),
			func(c *CacheRef) error { _, err := c.Get(ctx, "absent"); return err }},
		{"Get on unknown cache", "GET /apps/cache/api/caches/{name}/keys/{key...}", 404, unknownCacheBody,
			func(c *CacheRef) error { _, err := c.Get(ctx, "k"); return err }},
		{"Set on unknown cache", "PUT /apps/cache/api/caches/{name}/keys/{key...}", 404, unknownCacheBody,
			func(c *CacheRef) error { return c.Set(ctx, "k", "v", 0) }},
		{"Incr non-integer", "POST /apps/cache/api/caches/{name}/keys/incr/{key...}", 400, errResult("value is not an integer"),
			func(c *CacheRef) error { _, err := c.Incr(ctx, "word"); return err }},
		{"IncrWithTTL non-integer", "POST /apps/cache/api/caches/{name}/keys/incrttl/{key...}", 400, errResult("value is not an integer"),
			func(c *CacheRef) error { _, err := c.IncrWithTTL(ctx, "word", 1, time.Minute); return err }},
		{"Expire absent key", "POST /apps/cache/api/caches/{name}/keys/expire/{key...}", 404, errResult("key not found"),
			func(c *CacheRef) error { return c.Expire(ctx, "absent", time.Minute) }},
		{"Expire non-positive", "POST /apps/cache/api/caches/{name}/keys/expire/{key...}", 400, errResult("ttl is required and must be positive"),
			func(c *CacheRef) error { return c.Expire(ctx, "k", 0) }},
		{"SetNX server error", "POST /apps/cache/api/caches/{name}/keys/setnx/{key...}", 500, errResult("raft: not ready"),
			func(c *CacheRef) error { _, err := c.SetNX(ctx, "k", "v", 0); return err }},
		{"Score absent member", "GET /apps/cache/api/caches/{name}/zsets/{zset}/score/{member}", 404, errResult("member not found"),
			func(c *CacheRef) error { _, err := c.SortedSet("z").Score(ctx, "nobody"); return err }},
		{"Rank absent member", "GET /apps/cache/api/caches/{name}/zsets/{zset}/rank/{member}", 404, errResult("member not found"),
			func(c *CacheRef) error {
				rank, err := c.SortedSet("z").Rank(ctx, "nobody")
				if rank != -1 {
					return fmt.Errorf("Rank of an absent member = %d (err %v), want -1", rank, err)
				}
				return err
			}},
		{"LPop empty", "POST /apps/cache/api/caches/{name}/lists/{list}/lpop", 404, errResult("list is empty"),
			func(c *CacheRef) error { _, err := c.List("l").LPop(ctx); return err }},
		{"RPop empty", "POST /apps/cache/api/caches/{name}/lists/{list}/rpop", 404, errResult("list is empty"),
			func(c *CacheRef) error { _, err := c.List("l").RPop(ctx); return err }},
		{"Dequeue empty", "POST /apps/cache/api/caches/{name}/queues/{queue}/dequeue", 404, errResult("queue is empty"),
			func(c *CacheRef) error { _, err := c.Queue("q", 0).Dequeue(ctx); return err }},
		{"Queue Peek empty", "GET /apps/cache/api/caches/{name}/queues/{queue}", 404, errResult("queue is empty"),
			func(c *CacheRef) error { _, err := c.Queue("q", 0).Peek(ctx); return err }},
		{"Pop empty", "POST /apps/cache/api/caches/{name}/stacks/{stack}/pop", 404, errResult("stack is empty"),
			func(c *CacheRef) error { _, err := c.Stack("s", 0).Pop(ctx); return err }},
		{"Stack Peek empty", "GET /apps/cache/api/caches/{name}/stacks/{stack}", 404, errResult("stack is empty"),
			func(c *CacheRef) error { _, err := c.Stack("s", 0).Peek(ctx); return err }},
		{"Hash Get absent field", "GET /apps/cache/api/caches/{name}/hashes/{hash}/fields/{field}", 404, errResult("field not found"),
			func(c *CacheRef) error { _, err := c.Hash("h").Get(ctx, "nope"); return err }},
		{"FromJSON malformed", "PUT /apps/cache/api/caches/{name}/hashes/{hash}/json", 400, errResult("invalid JSON: invalid character 'n'"),
			func(c *CacheRef) error { return c.Hash("h").FromJSON(ctx, "{not json") }},
		{"Claim unknown group", "POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/claim", 404, errResult(`not found: group \"nope\" on channel \"work\"`),
			func(c *CacheRef) error { _, err := c.PubSub("work", PubSubOpts{}).Claim(ctx, "nope", "w1"); return err }},
		{"Trim non-positive", "POST /apps/cache/api/caches/{name}/pubsub/{channel}/trim", 400, errResult("max_age or max_size required"),
			func(c *CacheRef) error { _, err := c.PubSub("work", PubSubOpts{}).Trim(ctx, 0); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCacheFake(t)
			f.reply(tc.pattern, tc.status, tc.body)
			requireAPIStatus(t, tc.call(f.client.Cache("test")), tc.status)
		})
	}
}

func TestCacheRef_RejectsResponseWithoutEnvelope(t *testing.T) {
	f := newCacheFake(t)
	f.reply("GET /apps/cache/api/caches/{name}/keys/{key...}", 200, `{"key":"k","ttl_remaining":300,"value":"v"}`)
	f.reply("POST /apps/cache/api/caches/{name}/keys/setnx/{key...}", 200, `{"acquired":true,"key":"k"}`)
	c := f.client.Cache("test")
	ctx := context.Background()

	var apiErr *APIError
	if _, err := c.Get(ctx, "k"); err == nil || errors.As(err, &apiErr) {
		t.Fatalf("Get of an unwrapped body: err=%v, want a decode error", err)
	}
	if acquired, err := c.SetNX(ctx, "k", "v", 0); err == nil || acquired {
		t.Fatalf("SetNX of an unwrapped body = %v, %v; want a decode error", acquired, err)
	}
}

func TestSortedSetRef_DecodesCoreEnvelope(t *testing.T) {
	f := newCacheFake(t)
	f.reply("POST /apps/cache/api/caches/{name}/zsets/{zset}/add", 200, okResult(`{"added":0}`))
	f.reply("GET /apps/cache/api/caches/{name}/zsets/{zset}", 200, okResult(`{"count":2,"entries":[{"member":"bob","score":150},{"member":"alice","score":300}]}`))
	f.reply("GET /apps/cache/api/caches/{name}/zsets/{zset}/score/{member}", 200, okResult(`{"score":150}`))
	f.reply("GET /apps/cache/api/caches/{name}/zsets/{zset}/rank/{member}", 200, okResult(`{"rank":2}`))
	f.reply("GET /apps/cache/api/caches/{name}/zsets/{zset}/byscore", 200, okResult(`{"count":1,"entries":[{"member":"bob","score":150}]}`))
	f.reply("POST /apps/cache/api/caches/{name}/zsets/{zset}/rem", 200, okResult(`{"removed":1}`))
	f.reply("POST /apps/cache/api/caches/{name}/zsets/{zset}/incrby", 200, okResult(`{"member":"carol","score":202.5}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/zsets/{zset}", 200, okResult(`{"status":"deleted"}`))
	z := f.client.Cache("test").SortedSet("board")
	ctx := context.Background()

	added, err := z.Add(ctx, SortedSetEntry{Member: "bob", Score: 150})
	if err != nil || added != 0 {
		t.Fatalf("Add = %d, %v; want 0", added, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/zsets/{zset}/add", `{"entries":[{"member":"bob","score":150}]}`)
	all, err := z.Range(ctx, 0, -1)
	if err != nil || !slices.Equal(all, []SortedSetEntry{{"bob", 150}, {"alice", 300}}) {
		t.Fatalf("Range = %+v, %v", all, err)
	}
	if q := f.last(t).Query; q != "start=0&stop=-1" {
		t.Fatalf("Range query = %q", q)
	}
	if score, err := z.Score(ctx, "bob"); err != nil || score != 150 {
		t.Fatalf("Score = %v, %v; want 150", score, err)
	}
	if rank, err := z.Rank(ctx, "alice"); err != nil || rank != 2 {
		t.Fatalf("Rank = %d, %v; want 2", rank, err)
	}
	mid, err := z.RangeByScore(ctx, 150, 250.5)
	if err != nil || !slices.Equal(mid, []SortedSetEntry{{"bob", 150}}) {
		t.Fatalf("RangeByScore = %+v, %v", mid, err)
	}
	if q := f.last(t).Query; q != "max=250.5&min=150" {
		t.Fatalf("RangeByScore query = %q", q)
	}
	if removed, err := z.Rem(ctx, "dave", "nobody"); err != nil || removed != 1 {
		t.Fatalf("Rem = %d, %v; want 1", removed, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/zsets/{zset}/rem", `{"members":["dave","nobody"]}`)
	if score, err := z.IncrBy(ctx, "carol", 2.5); err != nil || score != 202.5 {
		t.Fatalf("IncrBy = %v, %v; want 202.5", score, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/zsets/{zset}/incrby", `{"member":"carol","delta":2.5}`)
	if err := z.Del(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestListSetQueueStack_DecodeCoreEnvelope(t *testing.T) {
	f := newCacheFake(t)
	f.reply("POST /apps/cache/api/caches/{name}/lists/{list}/lpush", 200, okResult(`{"length":3}`))
	f.reply("POST /apps/cache/api/caches/{name}/lists/{list}/rpush", 200, okResult(`{"length":2}`))
	f.reply("POST /apps/cache/api/caches/{name}/lists/{list}/lpop", 200, okResult(`{"value":"z"}`))
	f.reply("POST /apps/cache/api/caches/{name}/lists/{list}/rpop", 200, okResult(`{"value":"b"}`))
	f.reply("GET /apps/cache/api/caches/{name}/lists/{list}", 200, okResult(`{"length":3,"values":["z","a","b"]}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/lists/{list}", 200, okResult(`{"status":"deleted"}`))
	f.reply("POST /apps/cache/api/caches/{name}/sets/{set}/add", 200, okResult(`{"added":2}`))
	f.reply("POST /apps/cache/api/caches/{name}/sets/{set}/rem", 200, okResult(`{"removed":1}`))
	f.reply("GET /apps/cache/api/caches/{name}/sets/{set}", 200, okResult(`{"count":2,"members":["a","b"]}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/sets/{set}", 200, okResult(`{"status":"deleted"}`))
	f.reply("POST /apps/cache/api/caches/{name}/queues/{queue}/enqueue", 200, okResult(`{"length":2}`))
	f.reply("POST /apps/cache/api/caches/{name}/queues/{queue}/dequeue", 200, okResult(`{"value":"2"}`))
	f.reply("GET /apps/cache/api/caches/{name}/queues/{queue}", 200, okResult(`{"length":2,"value":"2"}`))
	f.reply("POST /apps/cache/api/caches/{name}/stacks/{stack}/push", 200, okResult(`{"length":2}`))
	f.reply("POST /apps/cache/api/caches/{name}/stacks/{stack}/pop", 200, okResult(`{"value":"c"}`))
	f.reply("GET /apps/cache/api/caches/{name}/stacks/{stack}", 200, okResult(`{"length":2,"value":"c"}`))
	c := f.client.Cache("test")
	ctx := context.Background()

	l := c.List("jobs")
	if n, err := l.RPush(ctx, "a", "b"); err != nil || n != 2 {
		t.Fatalf("RPush = %d, %v; want 2", n, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/lists/{list}/rpush", `{"values":["a","b"]}`)
	if n, err := l.LPush(ctx, "z"); err != nil || n != 3 {
		t.Fatalf("LPush = %d, %v; want 3", n, err)
	}
	if values, err := l.Range(ctx, 0, -1); err != nil || !slices.Equal(values, []string{"z", "a", "b"}) {
		t.Fatalf("Range = %q, %v", values, err)
	}
	if n, err := l.Len(ctx); err != nil || n != 3 {
		t.Fatalf("Len = %d, %v; want 3", n, err)
	}
	if v, err := l.LPop(ctx); err != nil || v != "z" {
		t.Fatalf("LPop = %q, %v; want z", v, err)
	}
	if v, err := l.RPop(ctx); err != nil || v != "b" {
		t.Fatalf("RPop = %q, %v; want b", v, err)
	}
	if err := l.Del(ctx); err != nil {
		t.Fatal(err)
	}

	s := c.SetStore("tags")
	if n, err := s.Add(ctx, "b", "a", "a"); err != nil || n != 2 {
		t.Fatalf("SetStore.Add = %d, %v; want 2", n, err)
	}
	if n, err := s.Rem(ctx, "a", "zz"); err != nil || n != 1 {
		t.Fatalf("SetStore.Rem = %d, %v; want 1", n, err)
	}
	if members, err := s.Members(ctx); err != nil || !slices.Equal(members, []string{"a", "b"}) {
		t.Fatalf("Members = %q, %v", members, err)
	}
	if err := s.Del(ctx); err != nil {
		t.Fatal(err)
	}

	q := c.Queue("q", 2)
	if n, err := q.Enqueue(ctx, "3"); err != nil || n != 2 {
		t.Fatalf("Enqueue = %d, %v; want 2", n, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/queues/{queue}/enqueue", `{"value":"3","max_size":2}`)
	if v, err := q.Peek(ctx); err != nil || v != "2" {
		t.Fatalf("Queue.Peek = %q, %v; want 2", v, err)
	}
	if v, err := q.Dequeue(ctx); err != nil || v != "2" {
		t.Fatalf("Dequeue = %q, %v; want 2", v, err)
	}

	st := c.Stack("s", 2)
	if n, err := st.Push(ctx, "c"); err != nil || n != 2 {
		t.Fatalf("Push = %d, %v; want 2", n, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/stacks/{stack}/push", `{"value":"c","max_size":2}`)
	if v, err := st.Peek(ctx); err != nil || v != "c" {
		t.Fatalf("Stack.Peek = %q, %v; want c", v, err)
	}
	if v, err := st.Pop(ctx); err != nil || v != "c" {
		t.Fatalf("Pop = %q, %v; want c", v, err)
	}
}

func TestHashRef_DecodesCoreEnvelope(t *testing.T) {
	f := newCacheFake(t)
	f.reply("PUT /apps/cache/api/caches/{name}/hashes/{hash}", 200, okResult(`{"status":"ok"}`))
	f.reply("GET /apps/cache/api/caches/{name}/hashes/{hash}", 200, okResult(`{"count":2,"fields":{"email":"a@example.com","name":"Alicia"}}`))
	f.reply("GET /apps/cache/api/caches/{name}/hashes/{hash}/fields/{field}", 200, okResult(`{"value":"Alicia"}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/hashes/{hash}", 200, okResult(`{"status":"deleted"}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/hashes/{hash}/fields/{field...}", 200, okResult(`{"removed":1}`))
	f.reply("GET /apps/cache/api/caches/{name}/hashes/{hash}/json", 200, okResult(`{"json":"{\"name\":\"Alicia\"}"}`))
	f.reply("PUT /apps/cache/api/caches/{name}/hashes/{hash}/json", 200, okResult(`{"status":"ok"}`))
	h := f.client.Cache("test").Hash("user:1")
	ctx := context.Background()

	if err := h.Set(ctx, map[string]string{"name": "Alicia"}); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "PUT", "PUT /apps/cache/api/caches/{name}/hashes/{hash}", `{"fields":{"name":"Alicia"}}`)
	if v, err := h.Get(ctx, "name"); err != nil || v != "Alicia" {
		t.Fatalf("Get = %q, %v; want Alicia", v, err)
	}
	all, err := h.GetAll(ctx)
	if err != nil || !maps.Equal(all, map[string]string{"name": "Alicia", "email": "a@example.com"}) {
		t.Fatalf("GetAll = %v, %v", all, err)
	}
	if err := h.Del(ctx, "email", "phone"); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).Field; got != "phone" {
		t.Fatalf("Del last field = %q, want phone", got)
	}
	if raw, err := h.ToJSON(ctx); err != nil || raw != `{"name":"Alicia"}` {
		t.Fatalf("ToJSON = %q, %v", raw, err)
	}
	if err := h.FromJSON(ctx, `{"city":"Lisbon"}`); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "PUT", "PUT /apps/cache/api/caches/{name}/hashes/{hash}/json", `{"json":"{\"city\":\"Lisbon\"}"}`)
	if err := h.DelAll(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPubSubRef_DecodesCoreEnvelope(t *testing.T) {
	f := newCacheFake(t)
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/publish", 200, okResult(`{"seq":7}`))
	f.reply("GET /apps/cache/api/caches/{name}/pubsub/{channel}/read", 200, okResult(`{"count":2,"messages":[{"seq":1,"value":"first","created_at":100},{"seq":2,"value":"second","created_at":101}]}`))
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}", 201, okResult(`{"status":"created"}`))
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/claim", 200, okResult(`{"message":{"seq":2,"value":"job-1","created_at":102}}`))
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/ack", 200, okResult(`{"status":"acked"}`))
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/reclaim", 200, okResult(`{"count":1,"messages":[{"seq":3,"value":"job-2","created_at":103}]}`))
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/trim", 200, okResult(`{"removed":0}`))
	f.reply("DELETE /apps/cache/api/caches/{name}/pubsub/{channel}", 200, okResult(`{"status":"deleted"}`))
	ch := f.client.Cache("test").PubSub("events", PubSubOpts{MaxSize: 2, MaxAge: time.Hour})
	ctx := context.Background()

	if seq, err := ch.Publish(ctx, "first"); err != nil || seq != 7 {
		t.Fatalf("Publish = %d, %v; want 7", seq, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/pubsub/{channel}/publish", `{"value":"first","max_size":2,"max_age_seconds":3600}`)
	msgs, err := ch.Read(ctx, "reader-1", 10)
	if err != nil || !slices.Equal(msgs, []PubSubMessage{{1, "first", 100}, {2, "second", 101}}) {
		t.Fatalf("Read = %+v, %v", msgs, err)
	}
	if q := f.last(t).Query; q != "consumer=reader-1&count=10" {
		t.Fatalf("Read query = %q", q)
	}
	if err := ch.CreateGroup(ctx, "workers"); err != nil {
		t.Fatal(err)
	}
	msg, err := ch.Claim(ctx, "workers", "w1")
	if err != nil || msg == nil || *msg != (PubSubMessage{2, "job-1", 102}) {
		t.Fatalf("Claim = %+v, %v", msg, err)
	}
	if err := ch.Ack(ctx, "workers", 2); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/ack", `{"seq":2}`)
	reclaimed, err := ch.Reclaim(ctx, "workers", "w3", 0)
	if err != nil || !slices.Equal(reclaimed, []PubSubMessage{{3, "job-2", 103}}) {
		t.Fatalf("Reclaim = %+v, %v", reclaimed, err)
	}
	if q := f.last(t).Query; q != "consumer=w3&timeout=0" {
		t.Fatalf("Reclaim query = %q", q)
	}
	if removed, err := ch.Trim(ctx, time.Hour); err != nil || removed != 0 {
		t.Fatalf("Trim = %d, %v; want 0", removed, err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches/{name}/pubsub/{channel}/trim", `{"max_age":3600}`)
	if err := ch.Del(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPubSubRef_ClaimFromExhaustedGroupIsNil(t *testing.T) {
	f := newCacheFake(t)
	f.reply("POST /apps/cache/api/caches/{name}/pubsub/{channel}/group/{group}/claim", 200, okResult(`{"message":null}`))
	msg, err := f.client.Cache("test").PubSub("work", PubSubOpts{}).Claim(context.Background(), "workers", "w1")
	if err != nil || msg != nil {
		t.Fatalf("Claim = %+v, %v; want nil, nil", msg, err)
	}
}

func TestClient_CacheManagement(t *testing.T) {
	f := newCacheFake(t)
	f.reply("POST /apps/cache/api/caches", 201, `{"created_at":1,"name":"new-cache"}`)
	f.reply("GET /apps/cache/api/caches", 200, okResult(`{"caches":[{"hit_rate":0,"key_count":0,"name":"localitas_public_http_paths"},{"hit_rate":50,"key_count":3,"name":"sessions"}]}`))
	f.reply("DELETE /apps/cache/api/caches/{name}", 200, `{"status":"deleted"}`)
	ctx := context.Background()

	if err := f.client.CreateCache(ctx, "new-cache"); err != nil {
		t.Fatal(err)
	}
	requireCall(t, f.last(t), "POST", "POST /apps/cache/api/caches", `{"name":"new-cache"}`)
	caches, err := f.client.ListCaches(ctx)
	want := []CacheInfo{{Name: "localitas_public_http_paths"}, {Name: "sessions", KeyCount: 3, HitRate: 50}}
	if err != nil || !slices.Equal(caches, want) {
		t.Fatalf("ListCaches = %+v, %v; want %+v", caches, err, want)
	}
	if err := f.client.DeleteCache(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_CacheManagementFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		pattern string
		status  int
		body    string
		call    func(*Client) error
	}{
		{"create duplicate", "POST /apps/cache/api/caches", 409, "cache already exists",
			func(c *Client) error { return c.CreateCache(ctx, "dup") }},
		{"delete absent", "DELETE /apps/cache/api/caches/{name}", 404, `cache "gone" not found`,
			func(c *Client) error { return c.DeleteCache(ctx, "gone") }},
		{"delete built-in", "DELETE /apps/cache/api/caches/{name}", 403, "cannot delete the localitas_public_http_paths cache",
			func(c *Client) error { return c.DeleteCache(ctx, "localitas_public_http_paths") }},
		{"list server error", "GET /apps/cache/api/caches", 500, "boom",
			func(c *Client) error { _, err := c.ListCaches(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCacheFake(t)
			f.reply(tc.pattern, tc.status, tc.body)
			requireAPIStatus(t, tc.call(f.client), tc.status)
		})
	}
}

func TestCacheRef_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, okResult(`{"status":"flushed"}`))
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("my-secret-token")
	if err := c.Cache("test").Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("auth = %q, want 'Bearer my-secret-token'", gotAuth)
	}
}
