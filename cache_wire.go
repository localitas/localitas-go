package client

// The cache app's answers, as core writes them and the SDK reads them. Each
// declares its fields in key order, so core writes the bytes it always has.

// CacheResponse is the envelope every cache data endpoint answers with: its
// result, or its error and a null result.
type CacheResponse[T any] struct {
	Error  string `json:"error"`
	Result T      `json:"result"`
}

// CacheList is the named caches on the server.
type CacheList struct {
	Caches []CacheInfo `json:"caches"`
}

// CacheKeyValue is a key read: its value and the seconds it has left (-1
// when it has no expiry).
type CacheKeyValue struct {
	Key          string `json:"key"`
	TTLRemaining int64  `json:"ttl_remaining"`
	Value        string `json:"value"`
}

// CacheKeys is the keys a pattern matched.
type CacheKeys struct {
	Keys []string `json:"keys"`
}

// CacheCounter is a counter key's value after an increment.
type CacheCounter struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

// CacheSetNX is whether a set-if-absent took the key.
type CacheSetNX struct {
	Acquired bool   `json:"acquired"`
	Key      string `json:"key"`
}

// CacheGetSet is a key's value before a get-and-set, when it had one.
type CacheGetSet struct {
	HadOld   bool   `json:"had_old"`
	Key      string `json:"key"`
	OldValue string `json:"old_value"`
}

// ListRange is a list's elements in a range and the list's length.
type ListRange struct {
	Length int64    `json:"length"`
	Values []string `json:"values"`
}

// PoppedValue is the element a queue or stack gave up and its length after.
type PoppedValue struct {
	Length int64  `json:"length"`
	Value  string `json:"value"`
}

// HashFields is a hash's fields.
type HashFields struct {
	Count  int               `json:"count"`
	Fields map[string]string `json:"fields"`
}

// SetMembers is a set's members, sorted.
type SetMembers struct {
	Count   int      `json:"count"`
	Members []string `json:"members"`
}

// SortedSetEntries is a sorted set's members in a range, lowest score first.
type SortedSetEntries struct {
	Count   int              `json:"count"`
	Entries []SortedSetEntry `json:"entries"`
}

// SortedSetScore is a member's score after an increment.
type SortedSetScore struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

// PubSubMessages is the messages a read or a reclaim delivered.
type PubSubMessages struct {
	Count    int             `json:"count"`
	Messages []PubSubMessage `json:"messages"`
}

// PubSubClaim is the message a claim delivered: nil when none was waiting.
type PubSubClaim struct {
	Message *PubSubMessage `json:"message"`
}
