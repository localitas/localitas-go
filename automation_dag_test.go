package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fixtureRequest is the golden request every SDK builds with its typed
// builders; core's strict decoder round-trips the same file
// (core/go/internal/apps/automation/testdata, which this copy must equal).
func fixtureRequest() CreateAutomationRequest {
	return CreateAutomationRequest{
		Name:        "nightly-sync",
		Description: "Sync, wait for its follow-ups, report",
		TriggerType: "periodic",
		IsEnabled:   true,
		TriggerConfig: TriggerConfig{
			Periodic:      &PeriodicTrigger{Schedule: "0 3 * * *", Timezone: "Europe/Paris", Catchup: CatchupAll, CatchupWindow: "48h"},
			Retries:       1,
			RetryDelay:    "5m",
			MaxActiveRuns: 2,
			SLA:           "2h",
		},
		DAGConfig: DAGConfig{
			DAGID:       "nightly-sync",
			Name:        "nightly-sync",
			Description: "Sync, wait for its follow-ups, report",
			Nodes: []DAGNode{
				HTTPNode("fetch", StrategyAnyNode, HTTPNodeSettings{
					App: &AppCall{App: "weather", Path: "/api/sync", Protocol: AppProtocolHTTP}, Method: "POST", Deferrable: Bool(false),
				}).WithTask(Task{Retries: Count(2), RetryDelay: "30s", Pool: "net", PoolSlots: 2, TriggerRule: RuleAllSuccess}),
				SensorNode("wait_event", StrategyAnyNode, EventSensor("invoice.paid").Within("1h")).
					WithTask(Task{Dependencies: []string{"fetch"}, DependsOnPast: true, TriggerRule: RuleAllDone}),
				SensorNode("wait_time", StrategyAnyNode, TimeSensor("15m")).WithTask(Task{Dependencies: []string{"fetch"}}),
				SensorNode("wait_file", StrategyAllNodes, FileSensor("/Documents/inbox")).WithTask(Task{Dependencies: []string{"fetch"}}),
				SensorNode("wait_paid", StrategyAnyNode, PubSubSensor("orders", map[string]json.RawMessage{
					"status": json.RawMessage(`"paid"`), "order.total": json.RawMessage(`100`),
				}).InCache("localitas_automations").Within("2h")).WithTask(Task{Dependencies: []string{"fetch"}, PriorityWeight: Count(10)}),
				HTTPNode("report", StrategyAnyNode, HTTPNodeSettings{URL: "https://example.com/hook", Body: json.RawMessage(`{"ok": true}`)}).
					WithTask(Task{Dependencies: []string{"wait_event", "wait_time", "wait_file", "wait_paid"}, TriggerRule: RuleOneSuccess}),
			},
		},
	}
}

// canonicalJSON is b with its object keys sorted and no spacing, so two
// encodings of the same value compare equal as strings.
func canonicalJSON(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestAutomationRequest_BuildsTheGoldenWireShape(t *testing.T) {
	want, err := os.ReadFile("testdata/sdk_automation_request.json")
	if err != nil {
		t.Fatal(err)
	}
	req := fixtureRequest()
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalJSON(t, got) != canonicalJSON(t, want) {
		t.Fatalf("request encodes as\n%s\nwant\n%s", got, want)
	}
}

// Phase D's fields: a task's priority weight and a pub/sub sensor.
func TestAutomationRequest_PriorityAndPubSubSensor(t *testing.T) {
	n := SensorNode("paid", StrategyAnyNode, PubSubSensor("orders", map[string]json.RawMessage{
		"status": json.RawMessage(`"paid"`), "order.total": json.RawMessage(`100`),
	}).InCache("shop").Within("2h")).WithTask(Task{PriorityWeight: Count(10)})
	got, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"node_id":"paid","node_type":"sensor","execution_strategy":"any_node","priority_weight":10,
		"metadata":{"wait_for":"pubsub","cache":"shop","channel":"orders","match":{"order.total":100,"status":"paid"},"timeout":"2h"}}`
	if canonicalJSON(t, got) != canonicalJSON(t, []byte(want)) {
		t.Fatalf("node encodes as %s", got)
	}
}

// loopbackURLs name the machine using them: core refuses each in an
// http-api node's url, as it sends whichever node runs the step to itself.
var loopbackURLs = []string{
	"http://localhost:9204/api/sync",
	"http://LOCALHOST:8090/apps/faas/fn/report",
	"http://127.0.0.2:9204/api/sync",
	"http://[::1]:8090/apps/notes/api/notes",
	"http://0.0.0.0:9204/api/sync",
}

// A url naming this machine is refused before anything is sent, naming the
// fix; a core path, another machine and a template are sent.
func TestHTTPNode_ALoopbackURLIsRefusedForAnAppCall(t *testing.T) {
	for _, raw := range loopbackURLs {
		req := fixtureRequest()
		req.DAGConfig.Nodes[0] = HTTPNode("fetch", StrategyAnyNode, HTTPNodeSettings{URL: raw})
		if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "AppCall") || !strings.Contains(err.Error(), "AppRef") {
			t.Errorf("%s: %v, want refused naming AppCall", raw, err)
		}
	}
	for _, raw := range []string{"/apps/faas/fn/report", "https://hooks.example.com/done", "http://192.168.1.50:8080/hook", "{{lookup.response.url}}"} {
		req := fixtureRequest()
		req.DAGConfig.Nodes[0] = HTTPNode("fetch", StrategyAnyNode, HTTPNodeSettings{URL: raw})
		if err := req.Validate(); err != nil {
			t.Errorf("%s: %v, want sent", raw, err)
		}
	}
}

func TestAutomationRequest_ValidateRefusesWhatCoreWould(t *testing.T) {
	bad := map[string]func(*CreateAutomationRequest){
		"strategy":       func(r *CreateAutomationRequest) { r.DAGConfig.Nodes[0].ExecutionStrategy = "everywhere" },
		"trigger rule":   func(r *CreateAutomationRequest) { r.DAGConfig.Nodes[0].TriggerRule = "most_success" },
		"retry delay":    func(r *CreateAutomationRequest) { r.DAGConfig.Nodes[0].RetryDelay = "soon" },
		"retries":        func(r *CreateAutomationRequest) { r.DAGConfig.Nodes[0].Retries = Count(11) },
		"pool slots":     func(r *CreateAutomationRequest) { r.DAGConfig.Nodes[0].PoolSlots = -1 },
		"catchup":        func(r *CreateAutomationRequest) { r.TriggerConfig.Periodic.Catchup = "some" },
		"catchup window": func(r *CreateAutomationRequest) { r.TriggerConfig.Periodic.CatchupWindow = "2 days" },
		"sla":            func(r *CreateAutomationRequest) { r.TriggerConfig.SLA = "late" },
		"sensor": func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[2] = SensorNode("t", StrategyAnyNode, TimeSensor("later"))
		},
		"wait for": func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[2] = SensorNode("t", StrategyAnyNode, Sensor{WaitFor: "moon"})
		},
		"url and app": func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[0] = HTTPNode("x", StrategyAnyNode, HTTPNodeSettings{URL: "https://a.test", App: &AppCall{App: "a", Path: "/b", Protocol: AppProtocolHTTP}})
		},
		"neither url nor app": func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[0] = HTTPNode("x", StrategyAnyNode, HTTPNodeSettings{})
		},
		"app path": func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[0] = HTTPNode("x", StrategyAnyNode, HTTPNodeSettings{App: &AppCall{App: "a", Path: "http://a.test/b", Protocol: AppProtocolHTTP}})
		},
		"not a url": func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[0] = HTTPNode("fetch", StrategyAnyNode, HTTPNodeSettings{URL: "ftp://a.test/b"})
		},
	}
	for _, raw := range loopbackURLs {
		bad["url "+raw] = func(r *CreateAutomationRequest) {
			r.DAGConfig.Nodes[0] = HTTPNode("fetch", StrategyAnyNode, HTTPNodeSettings{URL: raw})
		}
	}
	for name, spoil := range bad {
		req := fixtureRequest()
		spoil(&req)
		if err := req.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, req)
		}
	}
}
