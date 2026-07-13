package codexcaller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)

func TestThreeLayerCanaryFast(t *testing.T) {
	t.Run("success preserves exact and neutral evidence through typed decode", func(t *testing.T) {
		client, caller := canaryCaller(t, "success", codexsdk.ClientOptions{})
		defer closeCanary(t, client)

		result, err := llmadapter.ValueDetailed[struct {
			Answer bool `json:"answer"`
		}](context.Background(), caller, "answer")
		if err != nil {
			t.Fatal(err)
		}
		if !result.Value.Answer || result.Response.Execution.EffectiveModel != "canary-rerouted" {
			t.Fatalf("typed result = %#v", result)
		}
		if result.Response.Execution.Usage == nil || result.Response.Execution.Usage.InputTokens != 30 {
			t.Fatalf("neutral usage = %#v", result.Response.Execution.Usage)
		}
		details := result.Response.ProviderDetails.(Details)
		if details.Run.Run.FinalResponse != `{"answer":true}` || len(details.Run.Run.Notifications) != 4 || details.Run.Run.Usage.Total.OutputTokens != 20 {
			t.Fatalf("exact details = %#v", details.Run)
		}
	})

	t.Run("provider failure retains partial evidence at detailed and neutral layers", func(t *testing.T) {
		client, caller := canaryCaller(t, "provider-failure", codexsdk.ClientOptions{})
		defer client.Close()
		response, err := caller.Call(context.Background(), validRequest())
		if err == nil || response.FinalResponse != "partial" || response.Execution.ProviderName != "codex" || response.Execution.EffectiveModel != "canary-start" {
			t.Fatalf("response=%#v err=%v", response, err)
		}
		details := response.ProviderDetails.(Details)
		if details.Run.Run.Turn.Status != protocolv2.TurnStatusFailed || len(details.Run.Run.Notifications) < 2 {
			t.Fatalf("partial exact details = %#v", details.Run)
		}
		var turnErr *codexsdk.TurnError
		if !errors.Is(err, codexsdk.ErrTurnFailed) || !errors.As(err, &turnErr) {
			t.Fatalf("error=%v, want ErrTurnFailed and TurnError", err)
		}
	})

	t.Run("turn start failure retains thread start evidence", func(t *testing.T) {
		client, caller := canaryCaller(t, "turn-start-failure", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		response, err := caller.Call(context.Background(), validRequest())
		var protocolErr *codexsdk.ProtocolError
		if err == nil || !errors.As(err, &protocolErr) || protocolErr.Method != protocolv2.MethodTurnStart {
			t.Fatalf("response=%#v err=%v, want turn/start ProtocolError", response, err)
		}
		details := response.ProviderDetails.(Details)
		if details.Run.Start.Thread.ID != "thread-1" || details.Run.Run.Turn.ID != "" {
			t.Fatalf("post-thread/start evidence = %#v", details.Run)
		}
	})

	t.Run("typed decode failure retains successful call evidence", func(t *testing.T) {
		client, caller := canaryCaller(t, "decode-failure", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		result, err := llmadapter.ValueDetailed[map[string]bool](context.Background(), caller, "answer")
		if err == nil || result.Response.FinalResponse != "not-json" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if result.Response.ProviderDetails.(Details).Run.Run.Turn.Status != protocolv2.TurnStatusCompleted {
			t.Fatalf("decode failure erased exact run: %#v", result.Response)
		}
	})

	t.Run("read-only profile is sent and verified before projection", func(t *testing.T) {
		client, caller := canaryCaller(t, "success", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		if _, err := caller.CallDetailed(context.Background(), validRequest()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stream returns exact terminal evidence with effective profile error", func(t *testing.T) {
		client, caller := canaryCaller(t, "effective-profile-mismatch", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		stream, err := caller.CallStream(context.Background(), validRequest())
		if err != nil {
			t.Fatal(err)
		}
		if stream.SDKStream() == nil {
			t.Fatal("CallStream did not retain the typed SDK stream escape hatch")
		}
		run, err := stream.Wait(context.Background())
		if !errors.Is(err, ErrEffectiveProfile) {
			t.Fatalf("Wait error = %v, want ErrEffectiveProfile", err)
		}
		if run.Start.Thread.ID != "thread-1" || run.Start.Model != "canary-start" || run.Run.Turn.Status != protocolv2.TurnStatusCompleted || len(run.Run.Notifications) != 4 || run.Run.Usage == nil || run.Run.Usage.Total.OutputTokens != 20 {
			t.Fatalf("profile mismatch erased exact result: %#v", run)
		}
		if run.Start.ApprovalPolicy.Kind() != protocolv2.AskForApprovalKindOnRequest || run.Start.Sandbox.Kind() != protocolv2.SandboxPolicyKindDangerFullAccess || run.Start.Thread.Ephemeral {
			t.Fatalf("effective profile facts were not preserved: %#v", run.Start)
		}
	})
}

func TestEffectiveProfileContractAcrossPublicCallPaths(t *testing.T) {
	type profileCase struct {
		name               string
		scenario           string
		want               string
		wantEffectiveModel string
		missingThreadID    bool
	}
	profileCases := []profileCase{
		{name: "valid", scenario: "success", wantEffectiveModel: "canary-rerouted"},
		{name: "approval", scenario: "effective-profile-approval", want: "not never", wantEffectiveModel: "canary-rerouted"},
		{name: "sandbox", scenario: "effective-profile-sandbox", want: "not read-only", wantEffectiveModel: "canary-rerouted"},
		{name: "ephemeral", scenario: "effective-profile-ephemeral", want: "not ephemeral", wantEffectiveModel: "canary-rerouted"},
		{name: "missing-thread-id-approval", scenario: "missing-thread-id-approval", want: "not never", wantEffectiveModel: "canary-start", missingThreadID: true},
		{name: "missing-thread-id-sandbox", scenario: "missing-thread-id-sandbox", want: "not read-only", wantEffectiveModel: "canary-start", missingThreadID: true},
		{name: "missing-thread-id-ephemeral", scenario: "missing-thread-id-ephemeral", want: "not ephemeral", wantEffectiveModel: "canary-start", missingThreadID: true},
	}
	paths := []struct {
		name string
		call func(*testing.T, *Caller, profileCase) (codexsdk.StartedThreadRun, error)
	}{
		{name: "Call", call: func(t *testing.T, caller *Caller, profileCase profileCase) (codexsdk.StartedThreadRun, error) {
			t.Helper()
			response, err := caller.Call(context.Background(), validRequest())
			if response.Execution.ProviderName != "codex" || response.Execution.EffectiveModel != profileCase.wantEffectiveModel {
				t.Fatalf("neutral evidence = %#v, want decoded start projection", response.Execution)
			}
			details, ok := response.ProviderDetails.(Details)
			if !ok {
				t.Fatalf("provider details = %#v, want typed exact evidence", response.ProviderDetails)
			}
			return details.Run, err
		}},
		{name: "CallDetailed", call: func(_ *testing.T, caller *Caller, _ profileCase) (codexsdk.StartedThreadRun, error) {
			return caller.CallDetailed(context.Background(), validRequest())
		}},
		{name: "CallStream", call: func(t *testing.T, caller *Caller, _ profileCase) (codexsdk.StartedThreadRun, error) {
			t.Helper()
			stream, err := caller.CallStream(context.Background(), validRequest())
			if err != nil {
				return codexsdk.StartedThreadRun{}, err
			}
			run, waitErr := stream.Wait(context.Background())
			streamErr := stream.Err()
			if (waitErr == nil) != (streamErr == nil) || (waitErr != nil && streamErr.Error() != waitErr.Error()) {
				t.Fatalf("Err = %v, Wait error = %v, want stable terminal causes", streamErr, waitErr)
			}
			return run, waitErr
		}},
	}

	for _, profileCase := range profileCases {
		for _, path := range paths {
			t.Run(profileCase.name+"/"+path.name, func(t *testing.T) {
				client, caller := canaryCaller(t, profileCase.scenario, codexsdk.ClientOptions{})
				defer closeCanary(t, client)
				run, err := path.call(t, caller, profileCase)
				if profileCase.want == "" {
					if err != nil {
						t.Fatalf("call error = %v", err)
					}
				} else if profileCase.missingThreadID {
					requireMissingThreadProfileError(t, err, profileCase.want)
				} else if !errors.Is(err, ErrEffectiveProfile) || !strings.Contains(err.Error(), profileCase.want) {
					t.Fatalf("call error = %v, want ErrEffectiveProfile containing %q", err, profileCase.want)
				}
				if run.Start.Model != "canary-start" || run.Start.CWD != "/workspace" {
					t.Fatalf("decoded start evidence = %#v", run.Start)
				}
				if profileCase.missingThreadID {
					if run.Start.Thread.ID != "" || run.Run.Turn.ID != "" {
						t.Fatalf("exact partial run = %#v, want no lifecycle continuation", run)
					}
				} else if run.Start.Thread.ID != "thread-1" || run.Run.Turn.Status != protocolv2.TurnStatusCompleted || len(run.Run.Notifications) != 4 {
					t.Fatalf("exact terminal run = %#v", run)
				}
			})
		}
	}
}

func TestThreeLayerCanaryFull(t *testing.T) {
	if os.Getenv("LLMCALLER_FULL_CANARY") != "1" {
		t.Skip("set LLMCALLER_FULL_CANARY=1 for release/manual evidence")
	}

	t.Run("transport failure retains accepted partial evidence and first cause", func(t *testing.T) {
		client, caller := canaryCaller(t, "transport-failure", codexsdk.ClientOptions{})
		response, err := caller.Call(context.Background(), validRequest())
		if err == nil || !strings.Contains(err.Error(), "invalid app-server JSON-RPC") || !errors.Is(err, io.EOF) {
			t.Fatalf("response=%#v err=%v", response, err)
		}
		details := response.ProviderDetails.(Details)
		accepted, _ := json.Marshal(details.Run.Run.Notifications)
		if response.Execution.ProviderName != "codex" || response.Execution.EffectiveModel != "canary-start" || details.Run.Start.Thread.ID != "thread-1" || details.Run.Run.Turn.ID != "turn-1" || len(details.Run.Run.Notifications) != 1 || !strings.Contains(string(accepted), `"text":"partial"`) {
			t.Fatalf("transport failure erased partial evidence: %#v", response)
		}
		if closeErr := client.Close(); closeErr == nil || closeErr.Error() != err.Error() || !errors.Is(closeErr, io.EOF) {
			t.Fatalf("Close error=%v, first cause=%v", closeErr, err)
		}
	})

	t.Run("server request is typed and notification order is conserved", func(t *testing.T) {
		var mu sync.Mutex
		var kinds []protocolv2.ServerNotificationKind
		var requestKind protocolv2.ServerRequestKind
		options := codexsdk.ClientOptions{
			ServerNotificationHandler: func(_ context.Context, notification protocolv2.ServerNotification) error {
				mu.Lock()
				kinds = append(kinds, notification.Kind())
				mu.Unlock()
				return nil
			},
			ServerRequestHandler: func(_ context.Context, request protocolv2.ServerRequest) (codexsdk.ServerRequestResponse, error) {
				requestKind = request.Kind()
				return codexsdk.CommandExecutionApprovalResponse(protocolv2.CommandExecutionRequestApprovalResponse{
					Decision: protocolv2.NewCommandExecutionApprovalDecisionDecline(),
				}), nil
			},
		}
		client, caller := canaryCaller(t, "approval", options)
		defer closeCanary(t, client)
		run, err := caller.CallDetailed(context.Background(), validRequest())
		if err != nil {
			t.Fatal(err)
		}
		if requestKind != protocolv2.ServerRequestKindItemCommandExecutionRequestApproval {
			t.Fatalf("request kind = %s", requestKind)
		}
		mu.Lock()
		defer mu.Unlock()
		want := []protocolv2.ServerNotificationKind{
			protocolv2.ServerNotificationKindItemCompleted,
			protocolv2.ServerNotificationKindModelRerouted,
			protocolv2.ServerNotificationKindThreadTokenUsageUpdated,
			protocolv2.ServerNotificationKindTurnCompleted,
		}
		if !reflect.DeepEqual(kinds, want) || !reflect.DeepEqual(notificationKinds(run.Run.Notifications), want) {
			t.Fatalf("handler=%v run=%v want=%v", kinds, notificationKinds(run.Run.Notifications), want)
		}
	})

	t.Run("server request without handler declines safely", func(t *testing.T) {
		client, caller := canaryCaller(t, "approval", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		run, err := caller.CallDetailed(context.Background(), validRequest())
		if err != nil || run.Run.Turn.Status != protocolv2.TurnStatusCompleted {
			t.Fatalf("run=%#v err=%v", run, err)
		}
	})

	t.Run("pending and live notification order is conserved", func(t *testing.T) {
		client, caller := canaryCaller(t, "pending-live", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		run, err := caller.CallDetailed(context.Background(), validRequest())
		if err != nil {
			t.Fatal(err)
		}
		want := []protocolv2.ServerNotificationKind{
			protocolv2.ServerNotificationKindItemCompleted,
			protocolv2.ServerNotificationKindModelRerouted,
			protocolv2.ServerNotificationKindThreadTokenUsageUpdated,
			protocolv2.ServerNotificationKindTurnCompleted,
		}
		if got := notificationKinds(run.Run.Notifications); !reflect.DeepEqual(got, want) {
			t.Fatalf("notification order=%v want=%v", got, want)
		}
	})

	t.Run("notification attribution excludes another turn", func(t *testing.T) {
		client, caller := canaryCaller(t, "foreign-notification", codexsdk.ClientOptions{})
		defer closeCanary(t, client)
		run, err := caller.CallDetailed(context.Background(), validRequest())
		if err != nil {
			t.Fatal(err)
		}
		for _, notification := range run.Run.Notifications {
			raw, _ := json.Marshal(notification)
			if strings.Contains(string(raw), "turn-foreign") {
				t.Fatalf("foreign notification attributed to run: %s", raw)
			}
		}
	})

	t.Run("backpressure closes with stable typed cause", func(t *testing.T) {
		started := make(chan struct{})
		workdir := t.TempDir()
		startedPath := filepath.Join(workdir, canaryOverflowHandlerStarted)
		options := codexsdk.ClientOptions{
			CWD:                       workdir,
			NotificationQueueCapacity: 1,
			ServerNotificationHandler: func(ctx context.Context, _ protocolv2.ServerNotification) error {
				if err := os.WriteFile(startedPath, nil, 0o600); err != nil {
					return err
				}
				select {
				case <-started:
				default:
					close(started)
				}
				<-ctx.Done()
				return ctx.Err()
			},
		}
		client, caller := canaryCaller(t, "overflow", options)
		_, _ = caller.CallDetailed(context.Background(), validRequest())
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handler not started")
		}
		if err := client.Close(); !errors.Is(err, codexsdk.ErrNotificationBackpressure) {
			t.Fatalf("Close error=%v", err)
		}
	})

	t.Run("shutdown cancels and joins admitted request handler", func(t *testing.T) {
		started := make(chan struct{})
		finished := make(chan struct{})
		options := codexsdk.ClientOptions{ServerRequestHandler: func(ctx context.Context, _ protocolv2.ServerRequest) (codexsdk.ServerRequestResponse, error) {
			close(started)
			<-ctx.Done()
			close(finished)
			return codexsdk.ServerRequestResponse{}, ctx.Err()
		}}
		client, caller := canaryCaller(t, "approval-hang", options)
		stream, err := caller.CallStream(context.Background(), validRequest())
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handler not started")
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("handler not joined")
		}
		if !errors.Is(stream.Err(), codexsdk.ErrClientClosed) {
			t.Fatalf("stream error=%v", stream.Err())
		}
	})

	t.Run("handler failure remains the client first cause", func(t *testing.T) {
		handlerCause := errors.New("canary handler failure")
		options := codexsdk.ClientOptions{ServerRequestHandler: func(context.Context, protocolv2.ServerRequest) (codexsdk.ServerRequestResponse, error) {
			return codexsdk.ServerRequestResponse{}, handlerCause
		}}
		client, caller := canaryCaller(t, "approval-hang", options)
		response, err := caller.Call(context.Background(), validRequest())
		if !errors.Is(err, codexsdk.ErrHandlerFailed) || !errors.Is(err, handlerCause) {
			t.Fatalf("response=%#v err=%v", response, err)
		}
		if closeErr := client.Close(); !errors.Is(closeErr, codexsdk.ErrHandlerFailed) || !errors.Is(closeErr, handlerCause) {
			t.Fatalf("Close error=%v, want handler first cause", closeErr)
		}
	})
}

type canaryClient interface {
	ThreadRunner() codexsdk.ThreadRunner
	Close() error
}

func canaryCaller(t *testing.T, scenario string, options codexsdk.ClientOptions) (canaryClient, *Caller) {
	t.Helper()
	if options.CWD == "" {
		options.CWD = t.TempDir()
	}
	options.Command = []string{os.Args[0], "-test.run=TestThreeLayerFakeAppServer", "--", scenario}
	client, err := codexsdk.New(options)
	if err != nil {
		t.Fatalf("start fake app-server: %v", err)
	}
	caller, err := New(ReadOnlyEphemeralOptions(client.ThreadRunner()))
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client, caller
}

const canaryOverflowHandlerStarted = "overflow-handler-started"

func closeCanary(t *testing.T, client canaryClient) {
	t.Helper()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func notificationKinds(notifications []protocolv2.ServerNotification) []protocolv2.ServerNotificationKind {
	kinds := make([]protocolv2.ServerNotificationKind, len(notifications))
	for i := range notifications {
		kinds[i] = notifications[i].Kind()
	}
	return kinds
}

func TestThreeLayerFakeAppServer(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index < 0 || index >= len(os.Args) {
		return
	}
	runThreeLayerFakeAppServer(os.Args[index])
	os.Exit(0)
}

func runThreeLayerFakeAppServer(scenario string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		method, _ := message["method"].(string)
		id := message["id"]
		switch method {
		case "initialize":
			canarySend(map[string]any{"id": id, "result": map[string]any{"codexHome": "/tmp/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": "three-layer-canary"}})
		case "initialized":
		case "thread/start":
			params, _ := message["params"].(map[string]any)
			if params["ephemeral"] != true || params["sandbox"] != "read-only" {
				os.Exit(2)
			}
			result := canaryThreadStart()
			switch scenario {
			case "effective-profile-mismatch":
				result["approvalPolicy"] = "on-request"
				result["sandbox"] = map[string]any{"type": "dangerFullAccess"}
				result["thread"].(map[string]any)["ephemeral"] = false
			case "effective-profile-approval":
				result["approvalPolicy"] = "on-request"
			case "effective-profile-sandbox":
				result["sandbox"] = map[string]any{"type": "dangerFullAccess"}
			case "effective-profile-ephemeral":
				result["thread"].(map[string]any)["ephemeral"] = false
			case "missing-thread-id-approval":
				result["thread"].(map[string]any)["id"] = ""
				result["approvalPolicy"] = "on-request"
			case "missing-thread-id-sandbox":
				result["thread"].(map[string]any)["id"] = ""
				result["sandbox"] = map[string]any{"type": "dangerFullAccess"}
			case "missing-thread-id-ephemeral":
				result["thread"].(map[string]any)["id"] = ""
				result["thread"].(map[string]any)["ephemeral"] = false
			}
			canarySend(map[string]any{"id": id, "result": result})
		case "turn/start":
			params, _ := message["params"].(map[string]any)
			if params["approvalPolicy"] != "never" {
				os.Exit(3)
			}
			if scenario == "turn-start-failure" {
				canarySend(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "turn rejected"}})
				continue
			}
			if scenario == "pending-live" {
				canaryItem("thread-1", "turn-1", `{"answer":true}`)
			}
			canarySend(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "items": []any{}, "status": "inProgress"}}})
			switch scenario {
			case "approval", "approval-hang":
				canarySend(map[string]any{"id": "approval-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{"command": "echo no", "cwd": "/tmp", "itemId": "approval-item", "reason": "canary", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1"}})
			case "transport-failure":
				canaryItem("thread-1", "turn-1", "partial")
				fmt.Fprintln(os.Stdout, "{")
				return
			case "provider-failure":
				canaryItem("thread-1", "turn-1", "partial")
				canarySend(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "items": []any{canaryAgentItem("partial")}, "status": "failed", "error": map[string]any{"message": "provider failed"}}}})
			case "foreign-notification":
				canarySend(map[string]any{"method": "model/rerouted", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-foreign", "fromModel": "foreign-a", "toModel": "foreign-b", "reason": "highRiskCyberActivity"}})
				canaryComplete("success")
			case "overflow":
				canarySend(map[string]any{"method": "model/rerouted", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "fromModel": "initial", "toModel": "admitted", "reason": "highRiskCyberActivity"}})
				if !canaryWaitForFile(canaryOverflowHandlerStarted, 5*time.Second) {
					os.Exit(5)
				}
				for i := 0; i < 8; i++ {
					canarySend(map[string]any{"method": "model/rerouted", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "fromModel": fmt.Sprint(i), "toModel": fmt.Sprint(i + 1), "reason": "highRiskCyberActivity"}})
				}
			case "pending-live":
				canaryCompleteAfterItem(`{"answer":true}`)
			default:
				canaryComplete(scenario)
			}
		default:
			if method == "" && scenario == "approval" {
				result, _ := message["result"].(map[string]any)
				if result["decision"] != "decline" {
					os.Exit(4)
				}
				canaryComplete("success")
			}
		}
	}
}

func canaryWaitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func canaryThreadStart() map[string]any {
	return map[string]any{
		"approvalPolicy": "never", "approvalsReviewer": "user", "cwd": "/workspace", "model": "canary-start", "modelProvider": "openai",
		"sandbox": map[string]any{"type": "readOnly"},
		"thread":  map[string]any{"agentNickname": nil, "agentRole": nil, "cliVersion": "canary", "createdAt": 1, "cwd": "/workspace", "ephemeral": true, "forkedFromId": nil, "gitInfo": nil, "id": "thread-1", "modelProvider": "openai", "name": nil, "path": nil, "preview": "", "sessionId": "session-1", "source": "appServer", "status": map[string]any{"type": "idle"}, "threadSource": "user", "turns": []any{}, "updatedAt": 1},
	}
}

func canaryComplete(scenario string) {
	text := `{"answer":true}`
	if scenario == "decode-failure" {
		text = "not-json"
	}
	canaryItem("thread-1", "turn-1", text)
	canaryCompleteAfterItem(text)
}

func canaryCompleteAfterItem(text string) {
	canarySend(map[string]any{"method": "model/rerouted", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "fromModel": "canary-start", "toModel": "canary-rerouted", "reason": "highRiskCyberActivity"}})
	usage := map[string]any{"cachedInputTokens": 10, "inputTokens": 30, "outputTokens": 20, "reasoningOutputTokens": 5, "totalTokens": 50}
	canarySend(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tokenUsage": map[string]any{"last": usage, "total": usage}}})
	canarySend(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "items": []any{canaryAgentItem(text)}, "status": "completed"}}})
}

func canaryItem(threadID, turnID, text string) {
	canarySend(map[string]any{"method": "item/completed", "params": map[string]any{"completedAtMs": 1, "threadId": threadID, "turnId": turnID, "item": canaryAgentItem(text)}})
}

func canaryAgentItem(text string) map[string]any {
	return map[string]any{"id": "item-1", "type": "agentMessage", "text": text, "phase": "final_answer"}
}

func canarySend(message map[string]any) {
	raw, _ := json.Marshal(message)
	_, _ = os.Stdout.Write(append(raw, '\n'))
}
