package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/a2a"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/ui"
)

func TestTaskWiringInRoot(t *testing.T) {
	cmd := taskCmd()
	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	for _, expected := range []string{"place", "context", "run", "history", "logs", "delegate", "status", "approve", "reject"} {
		if !subcommands[expected] {
			t.Errorf("expected subcommand %q to be registered under task", expected)
		}
	}
}

func TestTaskDelegateCmd_AgentCardProbe(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent-card.json" {
			http.NotFound(w, r)
			return
		}
		card := a2a.AgentCard{
			Name:        "cachyos",
			Description: "AXIS compute node",
			Version:     "0.19.4",
			URL:         "http://cachyos:42425",
			Skills: []a2a.Skill{
				{ID: "axis-status", Name: "Status", Description: "Cluster status", Tags: []string{"observe"}},
				{ID: "guarded-exec", Name: "Exec", Description: "Guarded execution", Tags: []string{"exec"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	}))
	defer ts.Close()

	// Text mode
	var outBuf bytes.Buffer
	cmd := taskDelegateCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "--addr", ts.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("delegate probe Execute failed: %v", err)
	}

	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "Agent card for node cachyos") {
		t.Errorf("expected agent card header, got: %s", outStr)
	}
	if !strings.Contains(outStr, "axis-status") {
		t.Errorf("expected skill axis-status in output, got: %s", outStr)
	}

	// JSON mode
	outBuf.Reset()
	cmd = taskDelegateCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "--addr", ts.URL, "--format", "json"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("delegate probe JSON Execute failed: %v", err)
	}

	var cardResult a2a.AgentCard
	if err := json.Unmarshal(outBuf.Bytes(), &cardResult); err != nil {
		t.Fatalf("failed to unmarshal JSON card output: %v", err)
	}
	if cardResult.Name != "cachyos" {
		t.Errorf("expected card name cachyos, got %s", cardResult.Name)
	}
}

func TestTaskDelegateCmd_SendObserveSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a2a/v1/message:send" {
			http.NotFound(w, r)
			return
		}
		var req a2a.SendRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		task := a2a.Task{
			ID:      "task-obs-1",
			SkillID: req.SkillID,
			Status: a2a.TaskStatus{
				State:     a2a.TaskStateCompleted,
				Timestamp: time.Now(),
			},
			Artifacts: []a2a.Artifact{
				{
					Name:  "axis-status",
					Parts: []a2a.Part{{Type: "text", Text: `{"nodes": ["cranium", "cachyos"]}`}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	var outBuf bytes.Buffer
	cmd := taskDelegateCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "--skill", "axis-status", "--addr", ts.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("delegate send Execute failed: %v", err)
	}

	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "Task task-obs-1 on cachyos completed successfully") {
		t.Errorf("expected completion message, got: %s", outStr)
	}
	if !strings.Contains(outStr, "cranium") {
		t.Errorf("expected artifact text in output, got: %s", outStr)
	}
}

func TestTaskDelegateCmd_SendPendingApproval(t *testing.T) {
	var gotSkill string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a2a/v1/message:send" {
			http.NotFound(w, r)
			return
		}
		var req a2a.SendRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotSkill = req.SkillID

		task := a2a.Task{
			ID:      "task-pending-99",
			SkillID: req.SkillID,
			Status: a2a.TaskStatus{
				State:     a2a.TaskStatePending,
				Timestamp: time.Now(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	var outBuf bytes.Buffer
	cmd := taskDelegateCmd()
	cmd.SetOut(&outBuf)
	// Omitting --skill with a prompt should default to guarded-exec
	cmd.SetArgs([]string{"cachyos", "reboot system", "--addr", ts.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("delegate send pending Execute failed: %v", err)
	}

	if gotSkill != "guarded-exec" {
		t.Errorf("expected default skill guarded-exec, got %s", gotSkill)
	}

	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "Task task-pending-99 queued on cachyos (pending operator approval)") {
		t.Errorf("expected pending message, got: %s", outStr)
	}
	if !strings.Contains(outStr, "axis task approve cachyos task-pending-99 --confirm YES") {
		t.Errorf("expected approve hint in output, got: %s", outStr)
	}
}

func TestTaskDelegateCmd_SendRejectedAndFailed(t *testing.T) {
	stateToReturn := a2a.TaskStateRejected
	messageToReturn := "skill over tier"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		task := a2a.Task{
			ID: "task-bad-1",
			Status: a2a.TaskStatus{
				State: stateToReturn,
				Message: &a2a.Message{
					Parts: []a2a.Part{{Type: "text", Text: messageToReturn}},
				},
				Timestamp: time.Now(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	// 1. Rejected
	cmd := taskDelegateCmd()
	var outBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "bad command", "--addr", ts.URL})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for rejected task, got nil")
	}
	codeErr, ok := err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail, got %v", err)
	}
	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "rejected: skill over tier") {
		t.Errorf("expected rejected message, got: %s", outStr)
	}

	// 2. Failed
	stateToReturn = a2a.TaskStateFailed
	messageToReturn = "disk full"
	outBuf.Reset()

	cmd = taskDelegateCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "bad command", "--addr", ts.URL})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for failed task, got nil")
	}
	codeErr, ok = err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail, got %v", err)
	}
	outStr = ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "failed: disk full") {
		t.Errorf("expected failed message, got: %s", outStr)
	}
}

func TestTaskStatusCmd(t *testing.T) {
	taskToReturn := a2a.Task{
		ID:      "task-st-42",
		SkillID: "axis-status",
		Status: a2a.TaskStatus{
			State:     a2a.TaskStateCompleted,
			Timestamp: time.Date(2026, 9, 27, 18, 0, 0, 0, time.UTC),
			Message:   &a2a.Message{Parts: []a2a.Part{{Type: "text", Text: "all systems green"}}},
		},
		Artifacts: []a2a.Artifact{
			{Name: "health", Parts: []a2a.Part{{Type: "text", Text: "11/11 healthy"}}},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a2a/v1/tasks/task-st-42" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(taskToReturn)
	}))
	defer ts.Close()

	// Text format
	var outBuf bytes.Buffer
	cmd := taskStatusCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"samson", "task-st-42", "--addr", ts.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("task status Execute failed: %v", err)
	}

	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "Task ID:    task-st-42") {
		t.Errorf("expected Task ID in output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "State:      completed") {
		t.Errorf("expected completed state, got: %s", outStr)
	}
	if !strings.Contains(outStr, "11/11 healthy") {
		t.Errorf("expected artifact text in output, got: %s", outStr)
	}

	// JSON format
	outBuf.Reset()
	cmd = taskStatusCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"samson", "task-st-42", "--addr", ts.URL, "--format", "json"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("task status JSON Execute failed: %v", err)
	}

	var jsonTask a2a.Task
	if err := json.Unmarshal(outBuf.Bytes(), &jsonTask); err != nil {
		t.Fatalf("failed to unmarshal JSON task: %v", err)
	}
	if jsonTask.ID != "task-st-42" {
		t.Errorf("expected task-st-42, got %s", jsonTask.ID)
	}

	// 404 test
	outBuf.Reset()
	cmd = taskStatusCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"samson", "unknown-task", "--addr", ts.URL})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown task, got nil")
	}
}

func TestTaskApproveCmd(t *testing.T) {
	var gotBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a2a/v1/tasks/task-app-1/approve" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		task := a2a.Task{
			ID: "task-app-1",
			Status: a2a.TaskStatus{
				State:     a2a.TaskStateCompleted,
				Timestamp: time.Now(),
				Message:   &a2a.Message{Parts: []a2a.Part{{Type: "text", Text: "reboot initiated"}}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	// 1. Missing confirm
	cmd := taskApproveCmd()
	cmd.SetArgs([]string{"cachyos", "task-app-1", "--addr", ts.URL})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --confirm is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--confirm YES is required") {
		t.Errorf("expected confirm required error, got %v", err)
	}

	// 2. Invalid mode
	cmd = taskApproveCmd()
	cmd.SetArgs([]string{"cachyos", "task-app-1", "--confirm", "YES", "--mode", "invalid", "--addr", ts.URL})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --mode is invalid, got nil")
	}
	if !strings.Contains(err.Error(), "mode must be script or exec") {
		t.Errorf("expected mode validation error, got %v", err)
	}

	// 3. Success path
	var outBuf bytes.Buffer
	cmd = taskApproveCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "task-app-1", "--confirm", "YES", "--mode", "exec", "--addr", ts.URL})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("task approve Execute failed: %v", err)
	}

	if gotBody["confirm"] != execution.ConfirmWord || gotBody["mode"] != "exec" {
		t.Errorf("unexpected body received by server: %v", gotBody)
	}

	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "Task task-app-1 approved and completed successfully") {
		t.Errorf("expected completion message, got: %s", outStr)
	}
	if !strings.Contains(outStr, "reboot initiated") {
		t.Errorf("expected message text in output, got: %s", outStr)
	}
}

func TestTaskRejectCmd(t *testing.T) {
	var gotBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a2a/v1/tasks/task-rej-1/reject" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		task := a2a.Task{
			ID: "task-rej-1",
			Status: a2a.TaskStatus{
				State:     a2a.TaskStateRejected,
				Timestamp: time.Now(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	// 1. Missing reason
	cmd := taskRejectCmd()
	cmd.SetArgs([]string{"cachyos", "task-rej-1", "--addr", ts.URL})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --reason is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--reason is required") {
		t.Errorf("expected reason required error, got %v", err)
	}

	// 2. Success path
	var outBuf bytes.Buffer
	cmd = taskRejectCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "task-rej-1", "--reason", "risk too high", "--addr", ts.URL})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("task reject Execute failed: %v", err)
	}

	if gotBody["reason"] != "risk too high" {
		t.Errorf("unexpected body received by server: %v", gotBody)
	}

	outStr := ui.StripANSIAndControls(outBuf.String())
	if !strings.Contains(outStr, "Task task-rej-1 rejected: risk too high") {
		t.Errorf("expected rejected message, got: %s", outStr)
	}
}

func TestResolveA2AClient_LocalityAndAddr(t *testing.T) {
	// 1. Addr override takes precedence
	c1, err := resolveA2AClient("any-node", "http://custom:1234", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve with addr failed: %v", err)
	}
	if c1.BaseURL != "http://custom:1234" {
		t.Errorf("expected http://custom:1234, got %s", c1.BaseURL)
	}

	// 2. Local target resolves to socket
	c2, err := resolveA2AClient("local", "", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve local failed: %v", err)
	}
	if c2.BaseURL != "http://localhost" {
		t.Errorf("expected unix socket base URL http://localhost, got %s", c2.BaseURL)
	}

	// 3. Direct IP with port
	c3, err := resolveA2AClient("10.0.0.5:8080", "", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve direct IP failed: %v", err)
	}
	if c3.BaseURL != "http://10.0.0.5:8080" {
		t.Errorf("expected http://10.0.0.5:8080, got %s", c3.BaseURL)
	}
}

func TestTaskDelegateCmd_SendRejectedAndFailed_JSON(t *testing.T) {
	stateToReturn := a2a.TaskStateRejected
	messageToReturn := "skill over tier"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		task := a2a.Task{
			ID: "task-bad-json",
			Status: a2a.TaskStatus{
				State: stateToReturn,
				Message: &a2a.Message{
					Parts: []a2a.Part{{Type: "text", Text: messageToReturn}},
				},
				Timestamp: time.Now(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	// 1. Rejected with --format json
	cmd := taskDelegateCmd()
	var outBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "bad command", "--addr", ts.URL, "--format", "json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for rejected task in JSON mode, got nil")
	}
	codeErr, ok := err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail (code 4), got %v", err)
	}
	var resTask a2a.Task
	if err := json.Unmarshal(outBuf.Bytes(), &resTask); err != nil {
		t.Fatalf("expected valid JSON output on rejection, got err: %v, raw: %s", err, outBuf.String())
	}
	if resTask.Status.State != a2a.TaskStateRejected {
		t.Errorf("expected rejected task state in JSON output, got: %s", resTask.Status.State)
	}

	// 2. Failed with --format json
	stateToReturn = a2a.TaskStateFailed
	messageToReturn = "execution crash"
	outBuf.Reset()

	cmd = taskDelegateCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "bad command", "--addr", ts.URL, "--format", "json"})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for failed task in JSON mode, got nil")
	}
	codeErr, ok = err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail (code 4), got %v", err)
	}
	resTask = a2a.Task{}
	if err := json.Unmarshal(outBuf.Bytes(), &resTask); err != nil {
		t.Fatalf("expected valid JSON output on failure, got err: %v, raw: %s", err, outBuf.String())
	}
	if resTask.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected failed task state in JSON output, got: %s", resTask.Status.State)
	}
}

func TestTaskStatusCmd_RejectedAndFailed_JSON(t *testing.T) {
	stateToReturn := a2a.TaskStateRejected
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		task := a2a.Task{
			ID: "task-st-bad",
			Status: a2a.TaskStatus{
				State:     stateToReturn,
				Timestamp: time.Now(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	// 1. Rejected with --format json
	var outBuf bytes.Buffer
	cmd := taskStatusCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"samson", "task-st-bad", "--addr", ts.URL, "--format", "json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for rejected task in JSON mode, got nil")
	}
	codeErr, ok := err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail (code 4), got %v", err)
	}
	var res a2a.Task
	if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
		t.Fatalf("expected valid JSON on rejected status, got err: %v, raw: %s", err, outBuf.String())
	}
	if res.Status.State != a2a.TaskStateRejected {
		t.Errorf("expected rejected state, got %s", res.Status.State)
	}

	// 2. Failed with --format json
	stateToReturn = a2a.TaskStateFailed
	outBuf.Reset()
	cmd = taskStatusCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"samson", "task-st-bad", "--addr", ts.URL, "--format", "json"})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for failed task in JSON mode, got nil")
	}
	codeErr, ok = err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail (code 4), got %v", err)
	}
	res = a2a.Task{}
	if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
		t.Fatalf("expected valid JSON on failed status, got err: %v, raw: %s", err, outBuf.String())
	}
	if res.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected failed state, got %s", res.Status.State)
	}
}

func TestTaskApproveCmd_Failed_JSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		task := a2a.Task{
			ID: "task-app-fail",
			Status: a2a.TaskStatus{
				State:     a2a.TaskStateFailed,
				Timestamp: time.Now(),
				Message:   &a2a.Message{Parts: []a2a.Part{{Type: "text", Text: "exit code 127"}}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}))
	defer ts.Close()

	var outBuf bytes.Buffer
	cmd := taskApproveCmd()
	cmd.SetOut(&outBuf)
	cmd.SetArgs([]string{"cachyos", "task-app-fail", "--confirm", "YES", "--mode", "exec", "--addr", ts.URL, "--format", "json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when approved task execution fails in JSON mode, got nil")
	}
	codeErr, ok := err.(ExitCodeError)
	if !ok || codeErr.Code != ExitErrCommandFail {
		t.Errorf("expected ExitErrCommandFail (code 4), got %v", err)
	}
	var res a2a.Task
	if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
		t.Fatalf("expected valid JSON on failed approve output, got: %v", err)
	}
	if res.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected failed state, got %s", res.Status.State)
	}
}

func TestResolveA2AClient_RemoteNodeFromConfig(t *testing.T) {
	origLoad := loadA2AConfig
	defer func() { loadA2AConfig = origLoad }()

	loadA2AConfig = func() (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{
					Name:     "cachyos",
					Hostname: "192.168.1.50",
					Endpoints: []config.NodeEndpoint{
						{Name: "tailscale", Hostname: "100.64.0.5"},
					},
				},
				{
					Name:     "samson",
					Hostname: "192.168.1.60",
				},
			},
		}, nil
	}

	// 1. PrimaryHostname from Endpoints takes precedence
	c1, err := resolveA2AClient("cachyos", "", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve cachyos failed: %v", err)
	}
	if c1.BaseURL != "http://100.64.0.5:42425" {
		t.Errorf("expected http://100.64.0.5:42425, got %s", c1.BaseURL)
	}

	// 2. PrimaryHostname fallback to Hostname when no endpoints
	c2, err := resolveA2AClient("samson", "", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve samson failed: %v", err)
	}
	if c2.BaseURL != "http://192.168.1.60:42425" {
		t.Errorf("expected http://192.168.1.60:42425, got %s", c2.BaseURL)
	}

	// 3. Unknown remote node returns informative error
	_, err = resolveA2AClient("unknown-node", "", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for unknown node, got nil")
	}
	if !strings.Contains(err.Error(), "not found in") || !strings.Contains(err.Error(), "nodes.yaml") {
		t.Errorf("expected 'not found in ... nodes.yaml' error, got %v", err)
	}

	// 4. Local node still resolves to unix socket
	cLocal, err := resolveA2AClient("local", "", 5*time.Second)
	if err != nil {
		t.Fatalf("resolve local failed: %v", err)
	}
	if cLocal.BaseURL != "http://localhost" {
		t.Errorf("expected http://localhost, got %s", cLocal.BaseURL)
	}
}
