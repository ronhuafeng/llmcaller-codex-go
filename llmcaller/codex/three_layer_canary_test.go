package codexcaller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
		if err == nil || response.Execution.ProviderName != "codex" {
			t.Fatalf("response=%#v err=%v", response, err)
		}
		details := response.ProviderDetails.(Details)
		if details.Run.Run.Turn.Status != protocolv2.TurnStatusFailed || len(details.Run.Run.Notifications) < 2 {
			t.Fatalf("partial exact details = %#v", details.Run)
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
}

func TestThreeLayerCanaryFull(t *testing.T) {
	if os.Getenv("LLMCALLER_FULL_CANARY") != "1" {
		t.Skip("set LLMCALLER_FULL_CANARY=1 for release/manual evidence")
	}

	t.Run("transport failure retains accepted partial evidence and first cause", func(t *testing.T) {
		client, caller := canaryCaller(t, "transport-failure", codexsdk.ClientOptions{})
		response, err := caller.Call(context.Background(), validRequest())
		if err == nil || !strings.Contains(err.Error(), "invalid app-server JSON-RPC") {
			t.Fatalf("response=%#v err=%v", response, err)
		}
		if len(response.ProviderDetails.(Details).Run.Run.Notifications) != 1 {
			t.Fatalf("transport failure erased partial evidence: %#v", response)
		}
		if closeErr := client.Close(); closeErr == nil || closeErr.Error() != err.Error() {
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
		options := codexsdk.ClientOptions{
			NotificationQueueCapacity: 1,
			ServerNotificationHandler: func(ctx context.Context, _ protocolv2.ServerNotification) error {
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
}

func canaryCaller(t *testing.T, scenario string, options codexsdk.ClientOptions) (codexsdk.Client, *Caller) {
	t.Helper()
	options.CWD = t.TempDir()
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

func closeCanary(t *testing.T, client codexsdk.Client) {
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
			canarySend(map[string]any{"id": id, "result": canaryThreadStart()})
		case "turn/start":
			params, _ := message["params"].(map[string]any)
			if params["approvalPolicy"] != "never" {
				os.Exit(3)
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
				for i := 0; i < 8; i++ {
					canarySend(map[string]any{"method": "model/rerouted", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "fromModel": fmt.Sprint(i), "toModel": fmt.Sprint(i + 1), "reason": "highRiskCyberActivity"}})
				}
			default:
				canaryComplete(scenario)
			}
		default:
			if method == "" && scenario == "approval" {
				canaryComplete("success")
			}
		}
	}
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
