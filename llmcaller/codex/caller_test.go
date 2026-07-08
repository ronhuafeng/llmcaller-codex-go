package codexcaller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)

type fakeThreadStarter struct {
	requests []codexsdk.StartThreadRequest
	response codexsdk.ThreadRunResult
	err      error
}

func (starter *fakeThreadStarter) StartThread(ctx context.Context, request codexsdk.StartThreadRequest) (codexsdk.ThreadRunResult, error) {
	if err := ctx.Err(); err != nil {
		return codexsdk.ThreadRunResult{}, err
	}
	starter.requests = append(starter.requests, request)
	if starter.err != nil {
		return codexsdk.ThreadRunResult{}, starter.err
	}
	return starter.response, nil
}

func TestReadOnlyEphemeralOptions(t *testing.T) {
	starter := &fakeThreadStarter{}

	options := ReadOnlyEphemeralOptions(starter)

	if options.ThreadClient != starter {
		t.Fatalf("ThreadClient = %#v, want supplied starter", options.ThreadClient)
	}
	if options.ApprovalPolicy != codexsdk.ApprovalPolicyNever {
		t.Fatalf("ApprovalPolicy = %q, want %q", options.ApprovalPolicy, codexsdk.ApprovalPolicyNever)
	}
	if options.Ephemeral == nil || !*options.Ephemeral {
		t.Fatalf("Ephemeral = %#v, want true", options.Ephemeral)
	}

	options.Model = "gpt-5"
	options.CWD = "/tmp/work"
	options.Effort = codexsdk.ReasoningEffortLow

	if options.Model != "gpt-5" || options.CWD != "/tmp/work" || options.Effort != codexsdk.ReasoningEffortLow {
		t.Fatalf("returned options should remain mutable: %#v", options)
	}
}

func TestCallerAdaptsTypedRequestToCodexStartThread(t *testing.T) {
	starter := &fakeThreadStarter{response: codexsdk.ThreadRunResult{FinalResponse: `{"answer":true}`}}
	caller := New(Options{
		ThreadClient:      starter,
		Model:             "gpt-5",
		CWD:               "/tmp/work",
		Effort:            codexsdk.ReasoningEffortLow,
		ApprovalPolicy:    codexsdk.ApprovalPolicyNever,
		ApprovalsReviewer: codexsdk.ApprovalsReviewerUser,
		Ephemeral:         codexsdk.Bool(true),
	})

	response, err := caller.Call(context.Background(), llmadapter.Request{
		Prompt:       "answer as JSON",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"boolean"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.FinalResponse != `{"answer":true}` {
		t.Fatalf("final response = %q", response.FinalResponse)
	}
	if len(starter.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(starter.requests))
	}
	request := starter.requests[0]
	if len(request.Input) != 1 || request.Input[0].Text != "answer as JSON" {
		t.Fatalf("input = %#v", request.Input)
	}
	if request.Model != "gpt-5" || request.CWD != "/tmp/work" || request.Effort != codexsdk.ReasoningEffortLow {
		t.Fatalf("request options = %#v", request)
	}
	if request.Ephemeral == nil || !*request.Ephemeral {
		t.Fatalf("ephemeral = %#v", request.Ephemeral)
	}
	raw, err := request.OutputSchema.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"required":["answer"]`) {
		t.Fatalf("Codex output schema was not normalized to require all properties: %s", raw)
	}
}

func TestCallerWorksWithLLMAdapterValue(t *testing.T) {
	starter := &fakeThreadStarter{response: codexsdk.ThreadRunResult{FinalResponse: `true`}}
	caller := New(Options{ThreadClient: starter})

	got, err := llmadapter.Value[bool](context.Background(), caller, "Is Paris the capital of France?")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("Value returned false, want true")
	}
	raw, err := starter.requests[0].OutputSchema.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"boolean"`) {
		t.Fatalf("Codex output schema should describe bool output: %s", raw)
	}
}

func TestCallerFailsClosed(t *testing.T) {
	if _, err := (*Caller)(nil).Call(context.Background(), llmadapter.Request{OutputSchema: json.RawMessage(`true`)}); !errors.Is(err, ErrNilThreadClient) {
		t.Fatalf("nil caller error = %v, want ErrNilThreadClient", err)
	}

	caller := New(Options{ThreadClient: &fakeThreadStarter{}})
	if _, err := caller.Call(context.Background(), llmadapter.Request{}); !errors.Is(err, ErrMissingSchemaJSON) {
		t.Fatalf("missing schema error = %v, want ErrMissingSchemaJSON", err)
	}
	if _, err := caller.Call(context.Background(), llmadapter.Request{OutputSchema: json.RawMessage(`{"type":`)}); err == nil {
		t.Fatal("Call accepted invalid output schema JSON")
	}
}
