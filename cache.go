package client

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// CacheRef is a reference to a named cache. It provides Redis-like methods
// for key-value operations. Create one via Client.Cache("name").
//
// Usage:
//
//	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
//	sessions := c.Cache("sessions")
//	sessions.Set(ctx, "user:abc", `{"name":"Alice"}`, 30*time.Minute)
//	val, err := sessions.Get(ctx, "user:abc")
type CacheRef struct {
	client *Client
	name   string
}

// Cache returns a CacheRef bound to the named cache. The cache must already
// exist on the server (create it with CreateCache). All methods on CacheRef
// operate within this cache's isolated namespace.
func (c *Client) Cache(name string) *CacheRef {
	return &CacheRef{client: c, name: name}
}

// Get returns the value for a key. Returns an APIError with status 404
// if the key doesn't exist or has expired.
func (r *CacheRef) Get(ctx context.Context, key string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	path := fmt.Sprintf("/apps/cache/api/caches/%s/keys/%s", url.PathEscape(r.name), key)
	if err := r.client.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Set stores a value with an optional TTL. Pass 0 for no expiry.
func (r *CacheRef) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	path := fmt.Sprintf("/apps/cache/api/caches/%s/keys/%s", url.PathEscape(r.name), key)
	return r.client.do(ctx, "PUT", path, map[string]interface{}{
		"value": value,
		"ttl":   int(ttl.Seconds()),
	}, nil)
}

// Del removes a key from the cache.
func (r *CacheRef) Del(ctx context.Context, key string) error {
	path := fmt.Sprintf("/apps/cache/api/caches/%s/keys/%s", url.PathEscape(r.name), key)
	return r.client.do(ctx, "DELETE", path, nil, nil)
}

// Incr atomically increments a key's integer value by 1. If the key doesn't
// exist, it is created with value 1. Returns an error if the existing value
// is not an integer.
func (r *CacheRef) Incr(ctx context.Context, key string) (int64, error) {
	return r.IncrBy(ctx, key, 1)
}

// IncrBy atomically increments a key's integer value by delta.
func (r *CacheRef) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	var out struct {
		Value int64 `json:"value"`
	}
	path := fmt.Sprintf("/apps/cache/api/caches/%s/incr/%s", url.PathEscape(r.name), key)
	if err := r.client.do(ctx, "POST", path, map[string]interface{}{"delta": delta}, &out); err != nil {
		return 0, err
	}
	return out.Value, nil
}

// Keys returns all keys matching a glob pattern. Use "*" for all keys,
// "user:*" for prefix matching, "session:?" for single-character wildcard.
func (r *CacheRef) Keys(ctx context.Context, pattern string) ([]string, error) {
	var out struct {
		Keys []string `json:"keys"`
	}
	path := fmt.Sprintf("/apps/cache/api/caches/%s/keys?pattern=%s",
		url.PathEscape(r.name), url.QueryEscape(pattern))
	if err := r.client.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// Flush deletes all keys, lists, and sets from the cache and resets stats.
func (r *CacheRef) Flush(ctx context.Context) error {
	path := fmt.Sprintf("/apps/cache/api/caches/%s/flush", url.PathEscape(r.name))
	return r.client.do(ctx, "POST", path, nil, nil)
}

// Stats returns hit/miss counters, key count, and hit rate for the cache.
func (r *CacheRef) Stats(ctx context.Context) (*CacheStats, error) {
	var out CacheStats
	path := fmt.Sprintf("/apps/cache/api/caches/%s/stats", url.PathEscape(r.name))
	if err := r.client.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TTL returns the remaining time-to-live for a key. Returns -1 if the key
// has no expiry, -2 if the key doesn't exist.
func (r *CacheRef) TTL(ctx context.Context, key string) (time.Duration, error) {
	val, err := r.Get(ctx, key)
	_ = val
	if err != nil {
		return -2 * time.Second, nil
	}
	var out struct {
		TTLRemaining int `json:"ttl_remaining"`
	}
	path := fmt.Sprintf("/apps/cache/api/caches/%s/keys/%s", url.PathEscape(r.name), key)
	if err := r.client.do(ctx, "GET", path, nil, &out); err != nil {
		return -2 * time.Second, nil
	}
	return time.Duration(out.TTLRemaining) * time.Second, nil
}

// Expire sets or updates the TTL on an existing key. Returns an error
// if the key doesn't exist.
func (r *CacheRef) Expire(ctx context.Context, key string, ttl time.Duration) error {
	path := fmt.Sprintf("/apps/cache/api/caches/%s/expire/%s", url.PathEscape(r.name), key)
	return r.client.do(ctx, "POST", path, map[string]interface{}{
		"ttl": int(ttl.Seconds()),
	}, nil)
}

// --- List operations (double-headed deque) ---

// ListRef is a reference to a named list within a cache.
type ListRef struct {
	cache *CacheRef
	name  string
}

// List returns a ListRef for list operations within this cache.
func (r *CacheRef) List(name string) *ListRef {
	return &ListRef{cache: r, name: name}
}

// LPush prepends values to the head of the list. Returns the new length.
func (l *ListRef) LPush(ctx context.Context, values ...string) (int64, error) {
	var out struct{ Length int64 `json:"length"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s/lpush", url.PathEscape(l.cache.name), url.PathEscape(l.name))
	if err := l.cache.client.do(ctx, "POST", path, map[string]interface{}{"values": values}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// RPush appends values to the tail of the list. Returns the new length.
func (l *ListRef) RPush(ctx context.Context, values ...string) (int64, error) {
	var out struct{ Length int64 `json:"length"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s/rpush", url.PathEscape(l.cache.name), url.PathEscape(l.name))
	if err := l.cache.client.do(ctx, "POST", path, map[string]interface{}{"values": values}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// LPop removes and returns the first element.
func (l *ListRef) LPop(ctx context.Context) (string, error) {
	var out struct{ Value string `json:"value"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s/lpop", url.PathEscape(l.cache.name), url.PathEscape(l.name))
	if err := l.cache.client.do(ctx, "POST", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// RPop removes and returns the last element.
func (l *ListRef) RPop(ctx context.Context) (string, error) {
	var out struct{ Value string `json:"value"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s/rpop", url.PathEscape(l.cache.name), url.PathEscape(l.name))
	if err := l.cache.client.do(ctx, "POST", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Range returns elements from start to stop (inclusive, 0-based, negative indices from end).
func (l *ListRef) Range(ctx context.Context, start, stop int) ([]string, error) {
	var out struct{ Values []string `json:"values"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s?start=%d&stop=%d",
		url.PathEscape(l.cache.name), url.PathEscape(l.name), start, stop)
	if err := l.cache.client.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Values, nil
}

// Len returns the length of the list.
func (l *ListRef) Len(ctx context.Context) (int64, error) {
	var out struct{ Length int64 `json:"length"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s?start=0&stop=-1",
		url.PathEscape(l.cache.name), url.PathEscape(l.name))
	if err := l.cache.client.do(ctx, "GET", path, nil, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// Del deletes the entire list.
func (l *ListRef) Del(ctx context.Context) error {
	path := fmt.Sprintf("/apps/cache/api/caches/%s/list/%s", url.PathEscape(l.cache.name), url.PathEscape(l.name))
	return l.cache.client.do(ctx, "DELETE", path, nil, nil)
}

// --- Set operations ---

// SetRef is a reference to a named set within a cache.
type SetRef struct {
	cache *CacheRef
	name  string
}

// Set returns a SetRef for set operations within this cache.
func (r *CacheRef) SetStore(name string) *SetRef {
	return &SetRef{cache: r, name: name}
}

// Add adds members to the set. Returns the number of new members added.
func (s *SetRef) Add(ctx context.Context, members ...string) (int64, error) {
	var out struct{ Added int64 `json:"added"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/set/%s/add", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	if err := s.cache.client.do(ctx, "POST", path, map[string]interface{}{"members": members}, &out); err != nil {
		return 0, err
	}
	return out.Added, nil
}

// Rem removes members from the set. Returns the number removed.
func (s *SetRef) Rem(ctx context.Context, members ...string) (int64, error) {
	var out struct{ Removed int64 `json:"removed"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/set/%s/rem", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	if err := s.cache.client.do(ctx, "POST", path, map[string]interface{}{"members": members}, &out); err != nil {
		return 0, err
	}
	return out.Removed, nil
}

// Members returns all members of the set.
func (s *SetRef) Members(ctx context.Context) ([]string, error) {
	var out struct{ Members []string `json:"members"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/set/%s", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	if err := s.cache.client.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// Del deletes the entire set.
func (s *SetRef) Del(ctx context.Context) error {
	path := fmt.Sprintf("/apps/cache/api/caches/%s/set/%s", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	return s.cache.client.do(ctx, "DELETE", path, nil, nil)
}

// --- Queue operations (FIFO) ---

// QueueRef is a reference to a named queue within a cache.
type QueueRef struct {
	cache   *CacheRef
	name    string
	maxSize int
}

// Queue returns a QueueRef for FIFO queue operations. Pass 0 for unbounded.
func (r *CacheRef) Queue(name string, maxSize int) *QueueRef {
	return &QueueRef{cache: r, name: name, maxSize: maxSize}
}

// Enqueue adds a value to the back of the queue.
func (q *QueueRef) Enqueue(ctx context.Context, value string) (int64, error) {
	var out struct{ Length int64 `json:"length"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/queue/%s/enqueue", url.PathEscape(q.cache.name), url.PathEscape(q.name))
	if err := q.cache.client.do(ctx, "POST", path, map[string]interface{}{
		"value": value, "max_size": q.maxSize,
	}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// Dequeue removes and returns the front element (oldest).
func (q *QueueRef) Dequeue(ctx context.Context) (string, error) {
	var out struct{ Value string `json:"value"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/queue/%s/dequeue", url.PathEscape(q.cache.name), url.PathEscape(q.name))
	if err := q.cache.client.do(ctx, "POST", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Peek returns the front element without removing it.
func (q *QueueRef) Peek(ctx context.Context) (string, error) {
	var out struct{ Value string `json:"value"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/queue/%s", url.PathEscape(q.cache.name), url.PathEscape(q.name))
	if err := q.cache.client.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// --- Stack operations (LIFO) ---

// StackRef is a reference to a named stack within a cache.
type StackRef struct {
	cache   *CacheRef
	name    string
	maxSize int
}

// Stack returns a StackRef for LIFO stack operations. Pass 0 for unbounded.
func (r *CacheRef) Stack(name string, maxSize int) *StackRef {
	return &StackRef{cache: r, name: name, maxSize: maxSize}
}

// Push adds a value to the top of the stack.
func (s *StackRef) Push(ctx context.Context, value string) (int64, error) {
	var out struct{ Length int64 `json:"length"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/stack/%s/push", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	if err := s.cache.client.do(ctx, "POST", path, map[string]interface{}{
		"value": value, "max_size": s.maxSize,
	}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// Pop removes and returns the top element (newest).
func (s *StackRef) Pop(ctx context.Context) (string, error) {
	var out struct{ Value string `json:"value"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/stack/%s/pop", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	if err := s.cache.client.do(ctx, "POST", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Peek returns the top element without removing it.
func (s *StackRef) Peek(ctx context.Context) (string, error) {
	var out struct{ Value string `json:"value"` }
	path := fmt.Sprintf("/apps/cache/api/caches/%s/stack/%s", url.PathEscape(s.cache.name), url.PathEscape(s.name))
	if err := s.cache.client.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// --- Cache management (on Client) ---

// CacheStats holds hit/miss counters and derived metrics for a named cache.
type CacheStats struct {
	Name      string  `json:"name"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Sets      int64   `json:"sets"`
	Deletes   int64   `json:"deletes"`
	Evictions int64   `json:"evictions"`
	KeyCount  int64   `json:"key_count"`
	HitRate   float64 `json:"hit_rate"`
}

// CacheInfo describes a named cache instance.
type CacheInfo struct {
	Name     string `json:"name"`
	KeyCount int64  `json:"key_count"`
	HitRate  float64 `json:"hit_rate"`
}

// CreateCache creates a new named cache on the server. Returns an error
// if the cache already exists.
func (c *Client) CreateCache(ctx context.Context, name string) error {
	return c.do(ctx, "POST", "/apps/cache/api/caches", map[string]string{"name": name}, nil)
}

// ListCaches returns all named caches on the server.
func (c *Client) ListCaches(ctx context.Context) ([]CacheInfo, error) {
	var out struct {
		Caches []CacheInfo `json:"caches"`
	}
	if err := c.do(ctx, "GET", "/apps/cache/api/caches", nil, &out); err != nil {
		return nil, err
	}
	return out.Caches, nil
}

// DeleteCache deletes a named cache and all its data. The built-in
// "public_paths" cache cannot be deleted.
func (c *Client) DeleteCache(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/apps/cache/api/caches/"+url.PathEscape(name), nil, nil)
}
