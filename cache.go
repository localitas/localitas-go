package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// cacheEnvelope is the shape every cache data endpoint answers with.
type cacheEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// cacheDo calls a cache endpoint and decodes the result of its envelope into out.
func (c *Client) cacheDo(ctx context.Context, method, path string, body, out any) error {
	var env cacheEnvelope
	if err := c.do(ctx, method, path, body, &env); err != nil {
		return err
	}
	if len(env.Result) == 0 {
		return fmt.Errorf("decode %s %s: response has no result", method, path)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("decode %s %s result: %w (result=%s)", method, path, err, env.Result)
	}
	return nil
}

func (r *CacheRef) path(segments ...string) string {
	var b strings.Builder
	b.WriteString("/apps/cache/api/caches/")
	b.WriteString(url.PathEscape(r.name))
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}

func isKeyMiss(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	var env cacheEnvelope
	return json.Unmarshal([]byte(apiErr.Body), &env) == nil && env.Error == "key not found"
}

func ttlSeconds(ttl time.Duration) int {
	return int(ttl.Seconds())
}

type cacheValueRequest struct {
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
}

type cacheDeltaRequest struct {
	Delta int64 `json:"delta"`
}

type cacheDeltaTTLRequest struct {
	Delta int64 `json:"delta"`
	TTL   int   `json:"ttl"`
}

type cacheTTLRequest struct {
	TTL int `json:"ttl"`
}

type cacheValueResult struct {
	Value string `json:"value"`
}

type cacheCountResult struct {
	Value int64 `json:"value"`
}

// Get returns the value for a key. Returns an APIError with status 404
// if the key doesn't exist or has expired.
func (r *CacheRef) Get(ctx context.Context, key string) (string, error) {
	var out cacheValueResult
	if err := r.client.cacheDo(ctx, "GET", r.path("keys", key), nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Set stores a value with an optional TTL. Pass 0 for no expiry.
func (r *CacheRef) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.cacheDo(ctx, "PUT", r.path("keys", key), cacheValueRequest{Value: value, TTL: ttlSeconds(ttl)}, nil)
}

// Del removes a key from the cache. Deleting an absent key is not an error.
func (r *CacheRef) Del(ctx context.Context, key string) error {
	return r.client.cacheDo(ctx, "DELETE", r.path("keys", key), nil, nil)
}

// Incr atomically increments a key's integer value by 1. If the key doesn't
// exist, it is created with value 1. Returns an error if the existing value
// is not an integer.
func (r *CacheRef) Incr(ctx context.Context, key string) (int64, error) {
	return r.IncrBy(ctx, key, 1)
}

// IncrBy atomically increments a key's integer value by delta and returns
// the new value. A delta of 0 reads the value without changing it.
func (r *CacheRef) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	var out cacheCountResult
	if err := r.client.cacheDo(ctx, "POST", r.path("keys", "incr", key), cacheDeltaRequest{Delta: delta}, &out); err != nil {
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
	path := r.path("keys") + "?" + url.Values{"pattern": {pattern}}.Encode()
	if err := r.client.cacheDo(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// Flush deletes all keys, lists, and sets from the cache and resets stats.
func (r *CacheRef) Flush(ctx context.Context) error {
	return r.client.cacheDo(ctx, "POST", r.path("flush"), nil, nil)
}

// Stats returns hit/miss counters, key count, and hit rate for the cache.
func (r *CacheRef) Stats(ctx context.Context) (*CacheStats, error) {
	var out CacheStats
	if err := r.client.cacheDo(ctx, "GET", r.path("stats"), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TTL returns the remaining time-to-live for a key: -1s if the key has no
// expiry, -2s if the key doesn't exist or has expired. Any other failure,
// including an unknown cache, is returned as an error.
func (r *CacheRef) TTL(ctx context.Context, key string) (time.Duration, error) {
	var out struct {
		TTLRemaining int64 `json:"ttl_remaining"`
	}
	err := r.client.cacheDo(ctx, "GET", r.path("keys", key), nil, &out)
	if isKeyMiss(err) {
		return -2 * time.Second, nil
	}
	if err != nil {
		return 0, err
	}
	return time.Duration(out.TTLRemaining) * time.Second, nil
}

// Expire sets or updates the TTL on an existing key. Returns an APIError
// with status 404 if the key doesn't exist, 400 if ttl is not positive.
func (r *CacheRef) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.cacheDo(ctx, "POST", r.path("keys", "expire", key), cacheTTLRequest{TTL: ttlSeconds(ttl)}, nil)
}

// SetNX sets a key only if it does not already exist (or has expired).
// Returns true if the key was set, false if it already existed.
// Atomic — safe for distributed locks.
//
//	acquired, _ := cache.SetNX(ctx, "lock:resource", "owner-id", 30*time.Second)
//	if acquired { /* got the lock */ }
func (r *CacheRef) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	var out struct {
		Acquired bool `json:"acquired"`
	}
	if err := r.client.cacheDo(ctx, "POST", r.path("keys", "setnx", key), cacheValueRequest{Value: value, TTL: ttlSeconds(ttl)}, &out); err != nil {
		return false, err
	}
	return out.Acquired, nil
}

// GetSet atomically sets a key to a new value and returns the old value.
// Returns ("", false, nil) if the key didn't exist.
func (r *CacheRef) GetSet(ctx context.Context, key, value string, ttl time.Duration) (string, bool, error) {
	var out struct {
		OldValue string `json:"old_value"`
		HadOld   bool   `json:"had_old"`
	}
	if err := r.client.cacheDo(ctx, "POST", r.path("keys", "getset", key), cacheValueRequest{Value: value, TTL: ttlSeconds(ttl)}, &out); err != nil {
		return "", false, err
	}
	return out.OldValue, out.HadOld, nil
}

// IncrWithTTL atomically increments a key and sets its TTL only if the key
// is new. Existing keys keep their current TTL. Ideal for rate limiting:
//
//	count, _ := cache.IncrWithTTL(ctx, "rate:ip:1.2.3.4", 1, 60*time.Second)
//	if count > 100 { /* rate limited */ }
func (r *CacheRef) IncrWithTTL(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	var out cacheCountResult
	if err := r.client.cacheDo(ctx, "POST", r.path("keys", "incrttl", key), cacheDeltaTTLRequest{Delta: delta, TTL: ttlSeconds(ttl)}, &out); err != nil {
		return 0, err
	}
	return out.Value, nil
}

// --- Sorted Set operations ---

// SortedSetRef is a reference to a named sorted set within a cache.
// Members are ordered by score (ascending). Useful for leaderboards,
// priority queues, and time-window rate limiting.
type SortedSetRef struct {
	cache *CacheRef
	name  string
}

// SortedSetEntry represents a member with its score.
type SortedSetEntry struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

// SortedSet returns a SortedSetRef for sorted set operations.
//
//	leaderboard := cache.SortedSet("leaderboard")
//	leaderboard.Add(ctx, client.SortedSetEntry{Member: "player1", Score: 1500})
//	top10, _ := leaderboard.Range(ctx, 0, 9)
func (r *CacheRef) SortedSet(name string) *SortedSetRef {
	return &SortedSetRef{cache: r, name: name}
}

type cacheMembersRequest struct {
	Members []string `json:"members"`
}

type cacheValuesRequest struct {
	Values []string `json:"values"`
}

type cacheBoundedValueRequest struct {
	Value   string `json:"value"`
	MaxSize int    `json:"max_size"`
}

type cacheLengthResult struct {
	Length int64 `json:"length"`
}

type cacheAddedResult struct {
	Added int64 `json:"added"`
}

type cacheRemovedResult struct {
	Removed int64 `json:"removed"`
}

type cacheScoreResult struct {
	Score float64 `json:"score"`
}

type cacheEntriesResult struct {
	Entries []SortedSetEntry `json:"entries"`
}

type cacheMessagesResult struct {
	Messages []PubSubMessage `json:"messages"`
}

func rangeQuery(start, stop int) string {
	return "?" + url.Values{"start": {strconv.Itoa(start)}, "stop": {strconv.Itoa(stop)}}.Encode()
}

// Add adds or updates members with scores. Returns the number of new members
// added; updating an existing member's score adds none.
func (z *SortedSetRef) Add(ctx context.Context, entries ...SortedSetEntry) (int64, error) {
	var out cacheAddedResult
	body := struct {
		Entries []SortedSetEntry `json:"entries"`
	}{Entries: entries}
	if err := z.cache.client.cacheDo(ctx, "POST", z.cache.path("zsets", z.name, "add"), body, &out); err != nil {
		return 0, err
	}
	return out.Added, nil
}

// Score returns the score of a member. Returns an APIError with status 404
// if the member doesn't exist.
func (z *SortedSetRef) Score(ctx context.Context, member string) (float64, error) {
	var out cacheScoreResult
	if err := z.cache.client.cacheDo(ctx, "GET", z.cache.path("zsets", z.name, "score", member), nil, &out); err != nil {
		return 0, err
	}
	return out.Score, nil
}

// Rank returns the 0-based rank of a member (lowest score = rank 0). Returns
// -1 and an APIError with status 404 if the member doesn't exist.
func (z *SortedSetRef) Rank(ctx context.Context, member string) (int64, error) {
	var out struct {
		Rank int64 `json:"rank"`
	}
	if err := z.cache.client.cacheDo(ctx, "GET", z.cache.path("zsets", z.name, "rank", member), nil, &out); err != nil {
		return -1, err
	}
	return out.Rank, nil
}

// Range returns members by rank range (inclusive, 0-based, negative from end).
func (z *SortedSetRef) Range(ctx context.Context, start, stop int) ([]SortedSetEntry, error) {
	var out cacheEntriesResult
	if err := z.cache.client.cacheDo(ctx, "GET", z.cache.path("zsets", z.name)+rangeQuery(start, stop), nil, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// RangeByScore returns members with scores between min and max (inclusive).
func (z *SortedSetRef) RangeByScore(ctx context.Context, min, max float64) ([]SortedSetEntry, error) {
	var out cacheEntriesResult
	query := url.Values{
		"min": {strconv.FormatFloat(min, 'g', -1, 64)},
		"max": {strconv.FormatFloat(max, 'g', -1, 64)},
	}
	if err := z.cache.client.cacheDo(ctx, "GET", z.cache.path("zsets", z.name, "byscore")+"?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// Rem removes members. Returns the number removed.
func (z *SortedSetRef) Rem(ctx context.Context, members ...string) (int64, error) {
	var out cacheRemovedResult
	if err := z.cache.client.cacheDo(ctx, "POST", z.cache.path("zsets", z.name, "rem"), cacheMembersRequest{Members: members}, &out); err != nil {
		return 0, err
	}
	return out.Removed, nil
}

// IncrBy increments a member's score by delta. Creates with delta as score if new.
func (z *SortedSetRef) IncrBy(ctx context.Context, member string, delta float64) (float64, error) {
	var out cacheScoreResult
	body := struct {
		Member string  `json:"member"`
		Delta  float64 `json:"delta"`
	}{Member: member, Delta: delta}
	if err := z.cache.client.cacheDo(ctx, "POST", z.cache.path("zsets", z.name, "incrby"), body, &out); err != nil {
		return 0, err
	}
	return out.Score, nil
}

// Del deletes the entire sorted set.
func (z *SortedSetRef) Del(ctx context.Context) error {
	return z.cache.client.cacheDo(ctx, "DELETE", z.cache.path("zsets", z.name), nil, nil)
}

// --- DurablePubSub operations ---

// PubSubRef is a reference to a durable pub/sub channel within a cache.
// Supports both broadcast (every consumer sees every message) and consumer
// groups (round-robin with acknowledgment). Messages persist until trimmed.
//
// Broadcast:
//
//	ch := cache.PubSub("events", client.PubSubOpts{MaxSize: 1000})
//	ch.Publish(ctx, `{"type":"order.created"}`)
//	msgs, _ := ch.Read(ctx, "audit-service", 10)
//
// Consumer Group:
//
//	ch.CreateGroup(ctx, "workers")
//	msg, _ := ch.Claim(ctx, "workers", "worker-1")
//	process(msg)
//	ch.Ack(ctx, "workers", msg.Seq)
type PubSubRef struct {
	cache   *CacheRef
	channel string
	opts    PubSubOpts
}

// PubSubOpts controls retention for a durable pub/sub channel.
type PubSubOpts struct {
	// MaxSize bounds the channel by message count. 0 = unbounded by count.
	MaxSize int
	// MaxAge auto-expires messages older than this duration on each publish.
	// 0 = unbounded by age. Example: 14 * 24 * time.Hour = 2 weeks.
	MaxAge time.Duration
}

// PubSubMessage represents a published message with its sequence number.
type PubSubMessage struct {
	Seq       int64  `json:"seq"`
	Value     string `json:"value"`
	CreatedAt int64  `json:"created_at"`
}

// PubSub returns a PubSubRef for durable pub/sub operations on a channel.
//
//	// Bounded by count — keeps last 1000 messages
//	ch := cache.PubSub("events", client.PubSubOpts{MaxSize: 1000})
//
//	// Bounded by age — auto-expires after 2 weeks
//	ch := cache.PubSub("notifications", client.PubSubOpts{MaxAge: 14 * 24 * time.Hour})
//
//	// Both — whichever limit is hit first
//	ch := cache.PubSub("logs", client.PubSubOpts{MaxSize: 10000, MaxAge: 7 * 24 * time.Hour})
//
//	// Unbounded
//	ch := cache.PubSub("audit", client.PubSubOpts{})
func (r *CacheRef) PubSub(channel string, opts PubSubOpts) *PubSubRef {
	return &PubSubRef{cache: r, channel: channel, opts: opts}
}

func (p *PubSubRef) path(segments ...string) string {
	return p.cache.path(append([]string{"pubsub", p.channel}, segments...)...)
}

// Publish appends a message to the channel. Returns the sequence number.
// Bounded channels auto-trim by count (MaxSize) and/or age (MaxAge).
func (p *PubSubRef) Publish(ctx context.Context, value string) (int64, error) {
	var out struct {
		Seq int64 `json:"seq"`
	}
	body := struct {
		Value         string `json:"value"`
		MaxSize       int    `json:"max_size,omitempty"`
		MaxAgeSeconds int    `json:"max_age_seconds,omitempty"`
	}{Value: value, MaxSize: p.opts.MaxSize, MaxAgeSeconds: ttlSeconds(p.opts.MaxAge)}
	if err := p.cache.client.cacheDo(ctx, "POST", p.path("publish"), body, &out); err != nil {
		return 0, err
	}
	return out.Seq, nil
}

// Read returns new messages since this consumer's last read position.
// Each consumer tracks its own cursor independently (broadcast pattern).
// Automatically resumes from where it left off after reconnect.
func (p *PubSubRef) Read(ctx context.Context, consumerID string, count int) ([]PubSubMessage, error) {
	var out cacheMessagesResult
	query := url.Values{"consumer": {consumerID}, "count": {strconv.Itoa(count)}}
	if err := p.cache.client.cacheDo(ctx, "GET", p.path("read")+"?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// CreateGroup creates a consumer group. The group starts consuming from
// the current end of the channel (new messages only).
func (p *PubSubRef) CreateGroup(ctx context.Context, groupName string) error {
	return p.cache.client.cacheDo(ctx, "POST", p.path("group", groupName), nil, nil)
}

// Claim delivers the next unclaimed message to a consumer in a group
// (round-robin). The message is marked pending until Ack is called.
// Returns nil if no messages are available, and an APIError with status
// 404 if the group doesn't exist.
func (p *PubSubRef) Claim(ctx context.Context, groupName, consumerID string) (*PubSubMessage, error) {
	var out struct {
		Message *PubSubMessage `json:"message"`
	}
	query := url.Values{"consumer": {consumerID}}
	if err := p.cache.client.cacheDo(ctx, "POST", p.path("group", groupName, "claim")+"?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Message, nil
}

// Ack acknowledges that a message has been processed. Removes it from
// the pending list.
func (p *PubSubRef) Ack(ctx context.Context, groupName string, seq int64) error {
	body := struct {
		Seq int64 `json:"seq"`
	}{Seq: seq}
	return p.cache.client.cacheDo(ctx, "POST", p.path("group", groupName, "ack"), body, nil)
}

// Reclaim returns messages that have been pending longer than timeout
// without being acked. Re-assigns them to the given consumer.
func (p *PubSubRef) Reclaim(ctx context.Context, groupName, consumerID string, timeout time.Duration) ([]PubSubMessage, error) {
	var out cacheMessagesResult
	query := url.Values{"consumer": {consumerID}, "timeout": {strconv.Itoa(ttlSeconds(timeout))}}
	if err := p.cache.client.cacheDo(ctx, "POST", p.path("group", groupName, "reclaim")+"?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// Trim removes messages older than maxAge. Returns the number removed, or an
// APIError with status 400 if maxAge is not positive.
func (p *PubSubRef) Trim(ctx context.Context, maxAge time.Duration) (int64, error) {
	var out cacheRemovedResult
	body := struct {
		MaxAge int `json:"max_age"`
	}{MaxAge: ttlSeconds(maxAge)}
	if err := p.cache.client.cacheDo(ctx, "POST", p.path("trim"), body, &out); err != nil {
		return 0, err
	}
	return out.Removed, nil
}

// Del deletes the channel and all its messages, cursors, and groups.
func (p *PubSubRef) Del(ctx context.Context) error {
	return p.cache.client.cacheDo(ctx, "DELETE", p.path(), nil, nil)
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

func (l *ListRef) push(ctx context.Context, op string, values []string) (int64, error) {
	var out cacheLengthResult
	if err := l.cache.client.cacheDo(ctx, "POST", l.cache.path("lists", l.name, op), cacheValuesRequest{Values: values}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

func (l *ListRef) pop(ctx context.Context, op string) (string, error) {
	var out cacheValueResult
	if err := l.cache.client.cacheDo(ctx, "POST", l.cache.path("lists", l.name, op), nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// LPush prepends values to the head of the list. Returns the new length.
func (l *ListRef) LPush(ctx context.Context, values ...string) (int64, error) {
	return l.push(ctx, "lpush", values)
}

// RPush appends values to the tail of the list. Returns the new length.
func (l *ListRef) RPush(ctx context.Context, values ...string) (int64, error) {
	return l.push(ctx, "rpush", values)
}

// LPop removes and returns the first element. Returns an APIError with
// status 404 if the list is empty.
func (l *ListRef) LPop(ctx context.Context) (string, error) {
	return l.pop(ctx, "lpop")
}

// RPop removes and returns the last element. Returns an APIError with
// status 404 if the list is empty.
func (l *ListRef) RPop(ctx context.Context) (string, error) {
	return l.pop(ctx, "rpop")
}

type listRangeResult struct {
	Values []string `json:"values"`
	Length int64    `json:"length"`
}

// Range returns elements from start to stop (inclusive, 0-based, negative indices from end).
func (l *ListRef) Range(ctx context.Context, start, stop int) ([]string, error) {
	var out listRangeResult
	if err := l.cache.client.cacheDo(ctx, "GET", l.cache.path("lists", l.name)+rangeQuery(start, stop), nil, &out); err != nil {
		return nil, err
	}
	return out.Values, nil
}

// Len returns the length of the list.
func (l *ListRef) Len(ctx context.Context) (int64, error) {
	var out listRangeResult
	if err := l.cache.client.cacheDo(ctx, "GET", l.cache.path("lists", l.name)+rangeQuery(0, -1), nil, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// Del deletes the entire list.
func (l *ListRef) Del(ctx context.Context) error {
	return l.cache.client.cacheDo(ctx, "DELETE", l.cache.path("lists", l.name), nil, nil)
}

// --- Set operations ---

// SetRef is a reference to a named set within a cache.
type SetRef struct {
	cache *CacheRef
	name  string
}

// SetStore returns a SetRef for set operations within this cache.
func (r *CacheRef) SetStore(name string) *SetRef {
	return &SetRef{cache: r, name: name}
}

// Add adds members to the set. Returns the number of new members added.
func (s *SetRef) Add(ctx context.Context, members ...string) (int64, error) {
	var out cacheAddedResult
	if err := s.cache.client.cacheDo(ctx, "POST", s.cache.path("sets", s.name, "add"), cacheMembersRequest{Members: members}, &out); err != nil {
		return 0, err
	}
	return out.Added, nil
}

// Rem removes members from the set. Returns the number removed.
func (s *SetRef) Rem(ctx context.Context, members ...string) (int64, error) {
	var out cacheRemovedResult
	if err := s.cache.client.cacheDo(ctx, "POST", s.cache.path("sets", s.name, "rem"), cacheMembersRequest{Members: members}, &out); err != nil {
		return 0, err
	}
	return out.Removed, nil
}

// Members returns all members of the set, sorted.
func (s *SetRef) Members(ctx context.Context) ([]string, error) {
	var out struct {
		Members []string `json:"members"`
	}
	if err := s.cache.client.cacheDo(ctx, "GET", s.cache.path("sets", s.name), nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// Del deletes the entire set.
func (s *SetRef) Del(ctx context.Context) error {
	return s.cache.client.cacheDo(ctx, "DELETE", s.cache.path("sets", s.name), nil, nil)
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

// Enqueue adds a value to the back of the queue. Returns the new length; a
// bounded queue drops its oldest element on overflow.
func (q *QueueRef) Enqueue(ctx context.Context, value string) (int64, error) {
	var out cacheLengthResult
	if err := q.cache.client.cacheDo(ctx, "POST", q.cache.path("queues", q.name, "enqueue"), cacheBoundedValueRequest{Value: value, MaxSize: q.maxSize}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// Dequeue removes and returns the front element (oldest). Returns an
// APIError with status 404 if the queue is empty.
func (q *QueueRef) Dequeue(ctx context.Context) (string, error) {
	var out cacheValueResult
	if err := q.cache.client.cacheDo(ctx, "POST", q.cache.path("queues", q.name, "dequeue"), nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Peek returns the front element without removing it. Returns an APIError
// with status 404 if the queue is empty.
func (q *QueueRef) Peek(ctx context.Context) (string, error) {
	var out cacheValueResult
	if err := q.cache.client.cacheDo(ctx, "GET", q.cache.path("queues", q.name), nil, &out); err != nil {
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

// Push adds a value to the top of the stack. Returns the new length; a
// bounded stack drops its bottom element on overflow.
func (s *StackRef) Push(ctx context.Context, value string) (int64, error) {
	var out cacheLengthResult
	if err := s.cache.client.cacheDo(ctx, "POST", s.cache.path("stacks", s.name, "push"), cacheBoundedValueRequest{Value: value, MaxSize: s.maxSize}, &out); err != nil {
		return 0, err
	}
	return out.Length, nil
}

// Pop removes and returns the top element (newest). Returns an APIError
// with status 404 if the stack is empty.
func (s *StackRef) Pop(ctx context.Context) (string, error) {
	var out cacheValueResult
	if err := s.cache.client.cacheDo(ctx, "POST", s.cache.path("stacks", s.name, "pop"), nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// Peek returns the top element without removing it. Returns an APIError
// with status 404 if the stack is empty.
func (s *StackRef) Peek(ctx context.Context) (string, error) {
	var out cacheValueResult
	if err := s.cache.client.cacheDo(ctx, "GET", s.cache.path("stacks", s.name), nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// --- Hash operations ---

// HashRef is a reference to a named hash (field→value map) within a cache.
type HashRef struct {
	cache *CacheRef
	name  string
}

// Hash returns a HashRef for hash operations within this cache.
// Hashes store field→value maps — useful for objects without JSON overhead.
//
//	user := cache.Hash("user:123")
//	user.Set(ctx, map[string]string{"name": "Alice", "email": "alice@example.com"})
//	name, _ := user.Get(ctx, "name")
func (r *CacheRef) Hash(name string) *HashRef {
	return &HashRef{cache: r, name: name}
}

func (h *HashRef) path(segments ...string) string {
	return h.cache.path(append([]string{"hashes", h.name}, segments...)...)
}

// Set sets one or more field-value pairs. Upserts existing fields.
func (h *HashRef) Set(ctx context.Context, fields map[string]string) error {
	body := struct {
		Fields map[string]string `json:"fields"`
	}{Fields: fields}
	return h.cache.client.cacheDo(ctx, "PUT", h.path(), body, nil)
}

// Get returns the value of a single field. Returns an APIError with status
// 404 if the field doesn't exist.
func (h *HashRef) Get(ctx context.Context, field string) (string, error) {
	var out cacheValueResult
	if err := h.cache.client.cacheDo(ctx, "GET", h.path("fields", field), nil, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// GetAll returns all field-value pairs.
func (h *HashRef) GetAll(ctx context.Context) (map[string]string, error) {
	var out struct {
		Fields map[string]string `json:"fields"`
	}
	if err := h.cache.client.cacheDo(ctx, "GET", h.path(), nil, &out); err != nil {
		return nil, err
	}
	return out.Fields, nil
}

// Del removes one or more fields from the hash.
func (h *HashRef) Del(ctx context.Context, fields ...string) error {
	for _, field := range fields {
		if err := h.cache.client.cacheDo(ctx, "DELETE", h.path("fields", field), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// DelAll deletes the entire hash.
func (h *HashRef) DelAll(ctx context.Context) error {
	return h.cache.client.cacheDo(ctx, "DELETE", h.path(), nil, nil)
}

// ToJSON returns the hash as a JSON object string.
func (h *HashRef) ToJSON(ctx context.Context) (string, error) {
	var out struct {
		JSON string `json:"json"`
	}
	if err := h.cache.client.cacheDo(ctx, "GET", h.path("json"), nil, &out); err != nil {
		return "", err
	}
	return out.JSON, nil
}

// FromJSON sets hash fields from a JSON object string. Returns an APIError
// with status 400 if jsonStr is not a JSON object of strings.
func (h *HashRef) FromJSON(ctx context.Context, jsonStr string) error {
	body := struct {
		JSON string `json:"json"`
	}{JSON: jsonStr}
	return h.cache.client.cacheDo(ctx, "PUT", h.path("json"), body, nil)
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
	Name     string  `json:"name"`
	KeyCount int64   `json:"key_count"`
	HitRate  float64 `json:"hit_rate"`
}

// CreateCache creates a new named cache on the server. Returns an APIError
// with status 409 if the cache already exists.
func (c *Client) CreateCache(ctx context.Context, name string) error {
	body := struct {
		Name string `json:"name"`
	}{Name: name}
	return c.do(ctx, "POST", "/apps/cache/api/caches", body, nil)
}

// ListCaches returns all named caches on the server.
func (c *Client) ListCaches(ctx context.Context) ([]CacheInfo, error) {
	var out struct {
		Caches []CacheInfo `json:"caches"`
	}
	if err := c.cacheDo(ctx, "GET", "/apps/cache/api/caches", nil, &out); err != nil {
		return nil, err
	}
	return out.Caches, nil
}

// DeleteCache deletes a named cache and all its data. The built-in
// "localitas_public_http_paths" cache cannot be deleted.
func (c *Client) DeleteCache(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/apps/cache/api/caches/"+url.PathEscape(name), nil, nil)
}
