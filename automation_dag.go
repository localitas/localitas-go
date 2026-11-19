package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Where a node runs.
const (
	// StrategyRaftLeader runs the node on one node (any; the name is kept).
	StrategyRaftLeader = "raft_leader"
	// StrategyAnyNode runs the node on one node.
	StrategyAnyNode = "any_node"
	// StrategyAllNodes runs the node on every node, for per-node work.
	StrategyAllNodes = "all_nodes"
	// StrategyLocalOnly runs the node only on the node the automation names
	// (CreateAutomationRequest.NodeID), which calls its own: work on that
	// node's data, such as a folder on its disk. A cron job names no node,
	// so it cannot be local_only.
	StrategyLocalOnly = "local_only"
)

// What a task's upstreams must have done for it to run (Airflow's trigger
// rules); unset is RuleAllSuccess.
const (
	RuleAllSuccess  = "all_success"
	RuleAllDone     = "all_done"
	RuleOneSuccess  = "one_success"
	RuleOneFailed   = "one_failed"
	RuleNoneFailed  = "none_failed"
	CatchupLatest   = "latest"
	CatchupAll      = "all"
	SensorEvent     = "event"
	SensorFile      = "file"
	SensorTime      = "time"
	SensorPubSub    = "pubsub"
	NodeTypeHTTPAPI = "http-api"
	NodeTypeSensor  = "sensor"
)

// Bounds core holds an automation to (its validators).
const (
	maxTaskRetries     = 10
	maxRetryDelay      = 24 * time.Hour
	maxPoolSlots       = 100000
	maxActiveRunsBound = 1000
	maxCatchupWindow   = 7 * 24 * time.Hour
	maxSLA             = 30 * 24 * time.Hour
)

// DAGNode is one task of an automation: what it does (its type's settings,
// as JSON metadata: build one with HTTPNode or SensorNode) and the task
// settings of WithTask. Core refuses a metadata key the type does not have.
type DAGNode struct {
	NodeID            string          `json:"node_id"`
	NodeType          string          `json:"node_type"`
	ExecutionStrategy string          `json:"execution_strategy"`
	Dependencies      []string        `json:"dependencies,omitempty"`
	Retries           *int            `json:"retries,omitempty"`
	RetryDelay        string          `json:"retry_delay,omitempty"`
	Pool              string          `json:"pool,omitempty"`
	PoolSlots         int             `json:"pool_slots,omitempty"`
	PriorityWeight    *int            `json:"priority_weight,omitempty"`
	DependsOnPast     bool            `json:"depends_on_past,omitempty"`
	TriggerRule       string          `json:"trigger_rule,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// Task is a node's settings as an automation task, Airflow's: the tasks it
// waits for (Dependencies), its own retries (0-10; unset takes the
// automation's) and the wait before each (RetryDelay, "30s"), the pool
// whose PoolSlots it holds while it runs (default_pool when empty),
// PriorityWeight among the tasks waiting for that pool (higher first; unset
// is 1), DependsOnPast (runs once its task of the previous run succeeded)
// and TriggerRule.
type Task struct {
	Dependencies   []string
	Retries        *int
	RetryDelay     string
	Pool           string
	PoolSlots      int
	PriorityWeight *int
	DependsOnPast  bool
	TriggerRule    string
}

// WithTask is n with t's task settings.
func (n DAGNode) WithTask(t Task) DAGNode {
	n.Dependencies = t.Dependencies
	n.Retries = t.Retries
	n.RetryDelay = t.RetryDelay
	n.Pool = t.Pool
	n.PoolSlots = t.PoolSlots
	n.PriorityWeight = t.PriorityWeight
	n.DependsOnPast = t.DependsOnPast
	n.TriggerRule = t.TriggerRule
	return n
}

// AppProtocol is how an app endpoint is spoken to.
type AppProtocol string

const (
	AppProtocolHTTP AppProtocol = "http"
	AppProtocolWS   AppProtocol = "ws"
)

// AppCall names an app's endpoint: the app, a path on it, and the
// protocol. Core finds the app where it runs when the step runs; it is
// never a host or a port.
type AppCall struct {
	App      string      `json:"app"`
	Path     string      `json:"path"`
	Protocol AppProtocol `json:"protocol"`
}

func (a AppCall) validate() error {
	if !appNamePattern.MatchString(a.App) {
		return fmt.Errorf("app %q is not an app's name", a.App)
	}
	if !strings.HasPrefix(a.Path, "/") || strings.HasPrefix(a.Path, "//") || strings.Contains(a.Path, `\`) || strings.Contains(a.Path, "://") {
		return fmt.Errorf("app %s: %q is not a path", a.App, a.Path)
	}
	if a.Protocol != AppProtocolHTTP {
		return fmt.Errorf("app %s%s: an http-api step calls over http, not %q", a.App, a.Path, a.Protocol)
	}
	return nil
}

// HTTPNodeSettings is an http-api node's call: to App, an app's endpoint by
// name, or to URL, an external endpoint or a core path; exactly one. Unset
// fields take core's defaults: method POST, timeout 30s, 3 retries, backoff
// 2s ×2 up to 30s, expected status 200. Deferrable false keeps a call
// answered 202 waiting on its node (Airflow's deferrable=False); unset or
// true, the task defers and holds no worker.
type HTTPNodeSettings struct {
	URL               string            `json:"url,omitempty"`
	App               *AppCall          `json:"app,omitempty"`
	Method            string            `json:"method,omitempty"`
	ContentType       string            `json:"content_type,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	Body              json.RawMessage   `json:"body,omitempty"`
	TimeoutMs         int               `json:"timeout_ms,omitempty"`
	MaxRetries        *int              `json:"max_retries,omitempty"`
	BackoffMs         *int              `json:"backoff_ms,omitempty"`
	BackoffMultiplier float64           `json:"backoff_multiplier,omitempty"`
	MaxBackoffMs      int               `json:"max_backoff_ms,omitempty"`
	ExpectedStatus    int               `json:"expected_status,omitempty"`
	AsyncTimeoutMs    int               `json:"async_timeout_ms,omitempty"`
	Deferrable        *bool             `json:"deferrable,omitempty"`
	VaultCredentialID string            `json:"vault_credential_id,omitempty"`
	VaultAuthFormat   string            `json:"vault_auth_format,omitempty"`
	VaultHeaderName   string            `json:"vault_header_name,omitempty"`
	VaultHeaderPrefix string            `json:"vault_header_prefix,omitempty"`
}

func (s HTTPNodeSettings) validate() error {
	switch {
	case s.URL == "" && s.App == nil:
		return errors.New("http-api node needs url or app")
	case s.URL != "" && s.App != nil:
		return errors.New("http-api node names both url and app; it calls one of them")
	case s.App != nil:
		return s.App.validate()
	}
	return nil
}

// Count is n as an optional setting (MaxRetries, BackoffMs, Retries,
// PriorityWeight), where 0 is a value and not "use the default".
func Count(n int) *int { return &n }

// Bool is b as an optional setting (Deferrable).
func Bool(b bool) *bool { return &b }

// HTTPNode is an http-api node that makes the call s describes.
func HTTPNode(id, strategy string, s HTTPNodeSettings) DAGNode {
	// A struct of strings, ints and raw JSON always encodes.
	meta, _ := json.Marshal(s)
	return DAGNode{NodeID: id, NodeType: NodeTypeHTTPAPI, ExecutionStrategy: strategy, Metadata: meta}
}

// Sensor is what a sensor task waits for, holding no worker (Airflow's
// deferrable sensors): an event published to the automation app
// (EventSensor), a change under folders of the node it is pinned to
// (FileSensor, local_only or all_nodes), a time after its run's logical
// time (TimeSensor), or a message on a cache's pub/sub channel published
// after it started waiting whose JSON has every field of Match, a dotted
// name reaching into nested objects (PubSubSensor). Timeout bounds the wait
// (core's default 7 days).
type Sensor struct {
	WaitFor   string                     `json:"wait_for"`
	EventType string                     `json:"event_type,omitempty"`
	Paths     []string                   `json:"paths,omitempty"`
	Delay     string                     `json:"delay,omitempty"`
	Timeout   string                     `json:"timeout,omitempty"`
	Cache     string                     `json:"cache,omitempty"`
	Channel   string                     `json:"channel,omitempty"`
	Match     map[string]json.RawMessage `json:"match,omitempty"`
}

// EventSensor waits for an event of eventType.
func EventSensor(eventType string) Sensor { return Sensor{WaitFor: SensorEvent, EventType: eventType} }

// TimeSensor waits until delay ("30m") after its run's logical time.
func TimeSensor(delay string) Sensor { return Sensor{WaitFor: SensorTime, Delay: delay} }

// FileSensor waits for a change under one of paths.
func FileSensor(paths ...string) Sensor { return Sensor{WaitFor: SensorFile, Paths: paths} }

// PubSubSensor waits for a message on channel of the automations cache
// matching match (none: any message).
func PubSubSensor(channel string, match map[string]json.RawMessage) Sensor {
	return Sensor{WaitFor: SensorPubSub, Channel: channel, Match: match}
}

// Within is s waiting at most timeout ("2h").
func (s Sensor) Within(timeout string) Sensor {
	s.Timeout = timeout
	return s
}

// InCache is s listening on cache's channel instead of the automations
// cache's.
func (s Sensor) InCache(cache string) Sensor {
	s.Cache = cache
	return s
}

func (s Sensor) validate() error {
	switch s.WaitFor {
	case SensorEvent:
		if s.EventType == "" {
			return errors.New("an event sensor needs event_type")
		}
	case SensorFile:
		if len(s.Paths) == 0 {
			return errors.New("a file sensor needs paths")
		}
	case SensorTime:
		if err := checkDuration("delay", s.Delay, 0, 0); err != nil || s.Delay == "" {
			return fmt.Errorf("a time sensor needs delay, a duration (\"30m\"), got %q", s.Delay)
		}
	case SensorPubSub:
		if s.Channel == "" {
			return errors.New("a pubsub sensor needs channel")
		}
		for field, v := range s.Match {
			if field == "" || !json.Valid(v) {
				return fmt.Errorf("a pubsub sensor's match needs field names and JSON values, got %q: %s", field, v)
			}
		}
	default:
		return fmt.Errorf("sensor wait_for must be %s, %s, %s or %s, got %q", SensorEvent, SensorFile, SensorTime, SensorPubSub, s.WaitFor)
	}
	return checkDuration("sensor timeout", s.Timeout, time.Nanosecond, 0)
}

// SensorNode is a sensor node that waits for what s describes.
func SensorNode(id, strategy string, s Sensor) DAGNode {
	meta, _ := json.Marshal(s)
	return DAGNode{NodeID: id, NodeType: NodeTypeSensor, ExecutionStrategy: strategy, Metadata: meta}
}

type DAGConfig struct {
	DAGID       string    `json:"dag_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Nodes       []DAGNode `json:"nodes"`
}

// PeriodicTrigger runs an automation on Schedule (cron) read in Timezone
// (IANA; empty takes the cluster's default zone). Catchup is what runs of
// the slots that passed without a run: CatchupLatest (default) the newest,
// CatchupAll every one within CatchupWindow (default 24h, at most a week).
type PeriodicTrigger struct {
	Schedule       string `json:"schedule"`
	Timezone       string `json:"timezone,omitempty"`
	TimeoutMinutes int    `json:"timeout_minutes,omitempty"`
	Catchup        string `json:"catchup,omitempty"`
	CatchupWindow  string `json:"catchup_window,omitempty"`
}

// EventTrigger runs an automation on each event of EventType whose data
// has EventFilter's values; Paths limits a file event to changes under
// them.
type EventTrigger struct {
	EventType      string            `json:"event_type"`
	EventFilter    map[string]string `json:"event_filter,omitempty"`
	DebounceMs     int               `json:"debounce_ms,omitempty"`
	TimeoutMinutes int               `json:"timeout_minutes,omitempty"`
	Paths          []string          `json:"paths,omitempty"`
}

// TriggerConfig is when an automation runs and its run-wide settings:
// Retries (0-10) and RetryDelay for its tasks that name none,
// MaxActiveRuns (0: no bound) and SLA, how long after its logical time a
// run may take before its owner is told it missed it.
type TriggerConfig struct {
	Periodic      *PeriodicTrigger `json:"periodic,omitempty"`
	Event         *EventTrigger    `json:"event,omitempty"`
	Retries       int              `json:"retries,omitempty"`
	RetryDelay    string           `json:"retry_delay,omitempty"`
	MaxActiveRuns int              `json:"max_active_runs,omitempty"`
	SLA           string           `json:"sla,omitempty"`
}

type CreateAutomationRequest struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	DAGConfig     DAGConfig     `json:"dag_config"`
	TriggerType   string        `json:"trigger_type"`
	TriggerConfig TriggerConfig `json:"trigger_config"`
	IsEnabled     bool          `json:"is_enabled"`
	NotifyLevel   string        `json:"notify_level,omitempty"`
	// NodeID is the node a local_only automation runs on, the one holding
	// the data it works on; core refuses a local_only automation without
	// one, and a node for one with no local_only step.
	NodeID string `json:"node_id,omitempty"`
}

// checkDuration checks that d, when set, is a duration from least to most
// (0: unbounded).
func checkDuration(name, d string, least, most time.Duration) error {
	if d == "" {
		return nil
	}
	v, err := time.ParseDuration(d)
	if err != nil {
		return fmt.Errorf("%s %q is not a duration (\"30s\", \"5m\")", name, d)
	}
	if v < least || (most > 0 && v > most) {
		return fmt.Errorf("%s %s is out of range", name, d)
	}
	return nil
}

func checkRetries(prefix string, retries int, delay string) error {
	if retries < 0 || retries > maxTaskRetries {
		return fmt.Errorf("%sretries must be 0 to %d, got %d", prefix, maxTaskRetries, retries)
	}
	if retries == 0 && delay != "" {
		return fmt.Errorf("%sretry_delay %q is given without retries", prefix, delay)
	}
	return checkDuration(prefix+"retry_delay", delay, time.Second, maxRetryDelay)
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// decodeStrict reads raw into v, refusing a key v does not have, as core
// does with a node's metadata.
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Validate checks the request as core's API does before storing it, so a
// mistake is reported where it is made: strategies, trigger rules, retries
// and durations, pools, sensors, an http-api node's url or app, catch-up
// and limits.
func (r CreateAutomationRequest) Validate() error {
	t := r.TriggerConfig
	if err := checkRetries("", t.Retries, t.RetryDelay); err != nil {
		return err
	}
	if t.MaxActiveRuns < 0 || t.MaxActiveRuns > maxActiveRunsBound {
		return fmt.Errorf("max_active_runs must be 0 to %d, got %d", maxActiveRunsBound, t.MaxActiveRuns)
	}
	if err := checkDuration("sla", t.SLA, time.Minute, maxSLA); err != nil {
		return err
	}
	if p := t.Periodic; p != nil {
		if !oneOf(p.Catchup, "", CatchupLatest, CatchupAll) {
			return fmt.Errorf("catchup must be %s or %s, got %q", CatchupLatest, CatchupAll, p.Catchup)
		}
		if err := checkDuration("catchup_window", p.CatchupWindow, time.Minute, maxCatchupWindow); err != nil {
			return err
		}
	}
	for _, n := range r.DAGConfig.Nodes {
		if err := n.validate(); err != nil {
			return fmt.Errorf("task %s: %w", n.NodeID, err)
		}
	}
	return nil
}

func (n DAGNode) validate() error {
	if !oneOf(n.ExecutionStrategy, "", StrategyRaftLeader, StrategyAnyNode, StrategyAllNodes, StrategyLocalOnly) {
		return fmt.Errorf("execution_strategy %q is not one of %s, %s, %s, %s", n.ExecutionStrategy, StrategyRaftLeader, StrategyAnyNode, StrategyAllNodes, StrategyLocalOnly)
	}
	if !oneOf(n.TriggerRule, "", RuleAllSuccess, RuleAllDone, RuleOneSuccess, RuleOneFailed, RuleNoneFailed) {
		return fmt.Errorf("trigger_rule %q is not one of %s, %s, %s, %s, %s", n.TriggerRule, RuleAllSuccess, RuleAllDone, RuleOneSuccess, RuleOneFailed, RuleNoneFailed)
	}
	retries := 1
	if n.Retries != nil {
		retries = *n.Retries
	}
	if err := checkRetries("", retries, n.RetryDelay); err != nil {
		return err
	}
	if n.PoolSlots < 0 || n.PoolSlots > maxPoolSlots {
		return fmt.Errorf("pool_slots must be 1 to %d, got %d", maxPoolSlots, n.PoolSlots)
	}
	switch n.NodeType {
	case NodeTypeHTTPAPI:
		var s HTTPNodeSettings
		if err := decodeStrict(n.Metadata, &s); err != nil {
			return fmt.Errorf("http-api metadata: %w", err)
		}
		return s.validate()
	case NodeTypeSensor:
		var s Sensor
		if err := decodeStrict(n.Metadata, &s); err != nil {
			return fmt.Errorf("sensor metadata: %w", err)
		}
		return s.validate()
	}
	return nil
}
