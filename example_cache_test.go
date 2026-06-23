package client_test

import (
	"context"
	"fmt"
	"time"

	client "github.com/localitas/localitas-go"
)

// This file contains usage examples for the Cache API.
// These are documentation examples — they show how to use
// the SDK but don't run against a real server.

func Example_kvBasics() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()

	// Get a cache reference
	sessions := c.Cache("sessions")

	// Set a key with 30 minute TTL
	sessions.Set(ctx, "user:abc", `{"name":"Alice","role":"admin"}`, 30*time.Minute)

	// Get a key
	val, _ := sessions.Get(ctx, "user:abc")
	fmt.Println(val) // {"name":"Alice","role":"admin"}

	// Delete a key
	sessions.Del(ctx, "user:abc")

	// Set without TTL (permanent until deleted or flushed)
	sessions.Set(ctx, "config:theme", "dark", 0)
}

func Example_rateLimiting() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("rate_limiter")

	ip := "192.168.1.100"
	key := "rate:" + ip

	// IncrWithTTL: atomic increment + set TTL only on first call
	// Subsequent calls within the window just increment, TTL stays
	count, _ := cache.IncrWithTTL(ctx, key, 1, 60*time.Second)

	if count > 100 {
		fmt.Println("rate limited!")
	} else {
		fmt.Printf("request %d/100 this minute\n", count)
	}
}

func Example_distributedLock() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("locks")

	// Try to acquire a lock with 30 second TTL
	acquired, _ := cache.SetNX(ctx, "lock:order-processing", "worker-1", 30*time.Second)

	if acquired {
		fmt.Println("got the lock, processing...")
		// do work
		cache.Del(ctx, "lock:order-processing") // release
	} else {
		fmt.Println("lock held by another worker, skipping")
	}
}

func Example_atomicSwap() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("state")

	// GetSet: atomically replace a value and get the old one
	cache.Set(ctx, "deployment:status", "running", 0)

	old, hadOld, _ := cache.GetSet(ctx, "deployment:status", "completed", 0)
	if hadOld {
		fmt.Printf("status changed from %q to %q\n", old, "completed")
	}
}

func Example_patternSearch() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("app")

	cache.Set(ctx, "user:1:name", "Alice", 0)
	cache.Set(ctx, "user:2:name", "Bob", 0)
	cache.Set(ctx, "config:theme", "dark", 0)

	// Find all user keys
	userKeys, _ := cache.Keys(ctx, "user:*")
	fmt.Println(userKeys) // [user:1:name, user:2:name]

	// Single character wildcard
	singleChar, _ := cache.Keys(ctx, "user:?:name")
	fmt.Println(singleChar) // [user:1:name, user:2:name]
}

func Example_hash() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("app")

	// Hash: store structured data without JSON serialization
	user := cache.Hash("user:123")

	// Set multiple fields at once
	user.Set(ctx, map[string]string{
		"name":  "Alice",
		"email": "alice@example.com",
		"role":  "admin",
	})

	// Get a single field
	name, _ := user.Get(ctx, "name")
	fmt.Println(name) // Alice

	// Get all fields
	all, _ := user.GetAll(ctx)
	fmt.Println(all) // map[email:alice@example.com name:Alice role:admin]

	// Serialize to JSON (useful for API responses)
	jsonStr, _ := user.ToJSON(ctx)
	fmt.Println(jsonStr) // {"email":"alice@example.com","name":"Alice","role":"admin"}

	// Populate from JSON (useful for API inputs)
	user.FromJSON(ctx, `{"name":"Alice Smith","city":"NYC"}`)
}

func Example_list() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("app")

	// List: double-headed deque (push/pop from both ends)
	recent := cache.List("recent_searches")

	// Push to head (newest first)
	recent.LPush(ctx, "golang channels")
	recent.LPush(ctx, "sqlite wal mode")
	recent.LPush(ctx, "raft consensus")

	// Get last 10 searches
	searches, _ := recent.Range(ctx, 0, 9)
	fmt.Println(searches) // [raft consensus, sqlite wal mode, golang channels]

	// Pop from tail (remove oldest)
	oldest, _ := recent.RPop(ctx)
	fmt.Println(oldest) // golang channels
}

func Example_setStore() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("app")

	// Set: unique unordered collection
	tags := cache.SetStore("article:123:tags")

	tags.Add(ctx, "golang", "database", "sqlite", "golang") // duplicates ignored
	members, _ := tags.Members(ctx)
	fmt.Println(members) // [database, golang, sqlite] (sorted)

	tags.Rem(ctx, "sqlite")
}

func Example_sortedSet() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("game")

	// Sorted Set: members ordered by score
	leaderboard := cache.SortedSet("leaderboard")

	leaderboard.Add(ctx,
		client.SortedSetEntry{Member: "alice", Score: 1500},
		client.SortedSetEntry{Member: "bob", Score: 2100},
		client.SortedSetEntry{Member: "charlie", Score: 1800},
	)

	// Top 10 (highest scores = last entries since sorted ascending)
	top10, _ := leaderboard.Range(ctx, -10, -1)
	for _, entry := range top10 {
		fmt.Printf("%s: %.0f\n", entry.Member, entry.Score)
	}

	// Increment score (e.g., player wins a match)
	newScore, _ := leaderboard.IncrBy(ctx, "alice", 300)
	fmt.Printf("alice new score: %.0f\n", newScore) // 1800

	// Get player rank (0 = lowest score)
	rank, _ := leaderboard.Rank(ctx, "bob")
	fmt.Printf("bob rank: %d\n", rank)

	// Score range query
	midTier, _ := leaderboard.RangeByScore(ctx, 1500, 2000)
	fmt.Println(midTier) // alice and charlie
}

func Example_queue() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("app")

	// Queue: FIFO, bounded at 1000 jobs (oldest dropped on overflow)
	jobs := cache.Queue("background_jobs", 1000)

	// Enqueue work
	jobs.Enqueue(ctx, `{"type":"send_email","to":"alice@example.com"}`)
	jobs.Enqueue(ctx, `{"type":"generate_report","id":"monthly"}`)

	// Worker dequeues (oldest first)
	job, _ := jobs.Dequeue(ctx)
	fmt.Println(job) // send_email job

	// Peek without removing
	next, _ := jobs.Peek(ctx)
	fmt.Println(next) // generate_report job (still in queue)
}

func Example_stack() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("app")

	// Stack: LIFO, bounded at 50 (bottom dropped on overflow)
	undo := cache.Stack("undo_history", 50)

	undo.Push(ctx, `{"action":"delete","file":"doc.txt"}`)
	undo.Push(ctx, `{"action":"rename","from":"a.txt","to":"b.txt"}`)

	// Pop most recent action
	lastAction, _ := undo.Pop(ctx)
	fmt.Println(lastAction) // rename action (most recent)
}

func Example_durablePubSub_broadcast() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("events")

	// DurablePubSub: persistent channel with auto-expiry
	// Keeps last 1000 messages, expires after 2 weeks
	notifications := cache.PubSub("notifications", client.PubSubOpts{
		MaxSize: 1000,
		MaxAge:  14 * 24 * time.Hour,
	})

	// Publish events
	notifications.Publish(ctx, `{"type":"file.uploaded","user":"didip","file":"photo.jpg"}`)
	notifications.Publish(ctx, `{"type":"user.login","user":"alice"}`)

	// Broadcast read: each consumer sees ALL messages
	// Consumer cursor auto-advances — next call returns only new messages
	msgs, _ := notifications.Read(ctx, "ui-session-abc", 50)
	for _, msg := range msgs {
		fmt.Printf("[%d] %s\n", msg.Seq, msg.Value)
	}

	// Another consumer independently reads the same messages
	msgs2, _ := notifications.Read(ctx, "audit-log", 100)
	fmt.Printf("audit-log got %d messages\n", len(msgs2))
}

func Example_durablePubSub_consumerGroup() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()
	cache := c.Cache("work")

	// Consumer group: round-robin with acknowledgment
	jobs := cache.PubSub("email_jobs", client.PubSubOpts{MaxSize: 10000})

	// Create a consumer group (starts from current position)
	jobs.CreateGroup(ctx, "email_workers")

	// Publisher adds work
	jobs.Publish(ctx, `{"to":"alice@example.com","subject":"Welcome!"}`)
	jobs.Publish(ctx, `{"to":"bob@example.com","subject":"Invite"}`)

	// Worker claims next unclaimed message (round-robin across workers)
	msg, _ := jobs.Claim(ctx, "email_workers", "worker-1")
	if msg != nil {
		fmt.Printf("worker-1 processing: %s\n", msg.Value)

		// Process the email...
		sendEmail(msg.Value)

		// ACK: mark as processed (removes from pending)
		jobs.Ack(ctx, "email_workers", msg.Seq)
	}

	// If a worker dies without ACKing, reclaim its messages
	stuck, _ := jobs.Reclaim(ctx, "email_workers", "worker-2", 30*time.Second)
	for _, m := range stuck {
		fmt.Printf("reclaimed stuck message: seq=%d\n", m.Seq)
	}
}

func sendEmail(payload string) {
	// placeholder
}

func Example_cacheManagement() {
	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
	ctx := context.Background()

	// Create named caches (isolated databases)
	c.CreateCache(ctx, "sessions")
	c.CreateCache(ctx, "rate_limiter")
	c.CreateCache(ctx, "leaderboard")

	// List all caches
	caches, _ := c.ListCaches(ctx)
	for _, cache := range caches {
		fmt.Printf("%s: %d keys\n", cache.Name, cache.KeyCount)
	}

	// Delete a cache (public_paths cannot be deleted)
	c.DeleteCache(ctx, "leaderboard")

	// Flush all data in a cache (keeps the cache, removes all keys)
	c.Cache("sessions").Flush(ctx)
}
