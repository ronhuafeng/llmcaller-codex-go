package codexcaller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
	"github.com/ronhuafeng/llmkit-go/llmschema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type fakeRunner struct {
	requests       []codexsdk.StartThreadRunRequest
	streamRequests []codexsdk.StartThreadRunRequest
	result         codexsdk.StartedThreadRun
	err            error
	streamErr      error
}

type fakeStartedRunStream struct {
	result        codexsdk.StartedThreadRun
	err           error
	notifications []protocolv2.ServerNotification
	index         int
	current       protocolv2.ServerNotification
	closed        bool
}

func (stream *fakeStartedRunStream) Next(context.Context) bool {
	if stream.index >= len(stream.notifications) {
		return false
	}
	stream.current = stream.notifications[stream.index]
	stream.index++
	return true
}

func (stream *fakeStartedRunStream) Notification() protocolv2.ServerNotification {
	return stream.current
}

func (stream *fakeStartedRunStream) Wait(context.Context) (codexsdk.StartedThreadRun, error) {
	return stream.result, stream.err
}

func (stream *fakeStartedRunStream) Result() (codexsdk.StartedThreadRun, bool) {
	return stream.result, true
}

func (stream *fakeStartedRunStream) Err() error { return stream.err }

func (stream *fakeStartedRunStream) Close() error {
	stream.closed = true
	return nil
}

func (runner *fakeRunner) Start(ctx context.Context, request codexsdk.StartThreadRunRequest) (codexsdk.StartedThreadRun, error) {
	if err := ctx.Err(); err != nil {
		return codexsdk.StartedThreadRun{}, err
	}
	runner.requests = append(runner.requests, request)
	return runner.result, runner.err
}

func (runner *fakeRunner) StartStream(ctx context.Context, request codexsdk.StartThreadRunRequest) (*codexsdk.Stream[codexsdk.StartedThreadRun], error) {
	runner.streamRequests = append(runner.streamRequests, request)
	return nil, runner.streamErr
}

var _ ThreadRunner = (*fakeRunner)(nil)
var _ llmadapter.Caller = (*Caller)(nil)

type nullAwareString struct {
	SawNull bool
}

type typedProviderError struct {
	code string
}

func (err *typedProviderError) Error() string { return "provider terminal cause: " + err.code }

func (value *nullAwareString) UnmarshalJSON(data []byte) error {
	value.SawNull = string(data) == "null"
	return nil
}

func TestNewValidatesRunnerAndOwnedDefaults(t *testing.T) {
	if _, err := New(Options{}); !errors.Is(err, ErrNilThreadRunner) {
		t.Fatalf("New error = %v, want ErrNilThreadRunner", err)
	}
	var typedNil *fakeRunner
	if _, err := New(Options{Runner: typedNil}); !errors.Is(err, ErrNilThreadRunner) {
		t.Fatalf("typed nil error = %v", err)
	}
	runner := &fakeRunner{}
	tests := []codexsdk.StartThreadRunRequest{
		{Turn: protocolv2.TurnStartParams{ThreadID: "owned", Input: nil}},
		{Turn: protocolv2.TurnStartParams{Input: []protocolv2.UserInput{}}},
		{Turn: protocolv2.TurnStartParams{OutputSchema: outputSchemaPointer(t, `true`)}},
	}
	for _, defaults := range tests {
		if _, err := New(Options{Runner: runner, Defaults: defaults}); err == nil {
			t.Fatalf("New accepted conflicting defaults: %#v", defaults.Turn)
		}
	}
}

func TestCallerBuildsExactRequestAndProjectsEvidence(t *testing.T) {
	run := validStartedRun("final", "gpt-start")
	run.Run.Usage = &protocolv2.ThreadTokenUsage{Total: protocolv2.TokenUsageBreakdown{
		InputTokens: 11, CachedInputTokens: 3, OutputTokens: 5, ReasoningOutputTokens: 2,
	}}
	run.Run.Notifications = []protocolv2.ServerNotification{modelRerouted("gpt-start", "gpt-rerouted")}
	runner := &fakeRunner{result: run}
	defaults := codexsdk.StartThreadRunRequest{
		Thread: protocolv2.ThreadStartParams{
			Model:                 protocolv2.Value("gpt-request"),
			RuntimeWorkspaceRoots: protocolv2.Value([]string{"/workspace"}),
		},
		Turn: protocolv2.TurnStartParams{Effort: protocolv2.Value(protocolv2.ReasoningEffort("high"))},
	}
	caller, err := New(Options{Runner: runner, Defaults: defaults})
	if err != nil {
		t.Fatal(err)
	}
	response, err := caller.Call(context.Background(), llmadapter.Request{
		Prompt:       "answer as JSON",
		OutputSchema: json.RawMessage(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"boolean"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.FinalResponse != "final" || response.Execution.ProviderName != "codex" || response.Execution.EffectiveModel != "gpt-rerouted" {
		t.Fatalf("response = %#v", response)
	}
	if response.Execution.Usage == nil || response.Execution.Usage.InputTokens != 11 || response.Execution.Usage.ReasoningOutputTokens != 2 {
		t.Fatalf("neutral usage = %#v", response.Execution.Usage)
	}
	details, ok := response.ProviderDetails.(Details)
	if !ok || details.ProviderName() != "codex" || !reflect.DeepEqual(details.Run, run) {
		t.Fatalf("details = %#v", response.ProviderDetails)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("requests = %d", len(runner.requests))
	}
	request := runner.requests[0]
	if request.Turn.ThreadID != "" || len(request.Turn.Input) != 1 {
		t.Fatalf("adapter-owned turn fields = %#v", request.Turn)
	}
	text, ok := request.Turn.Input[0].AsText()
	if !ok || text.Text != "answer as JSON" || request.Turn.OutputSchema == nil {
		t.Fatalf("turn input/schema = %#v", request.Turn)
	}
	if request.Thread.Model == nil || request.Thread.Model.Value == nil || *request.Thread.Model.Value != "gpt-request" {
		t.Fatalf("exact defaults were not preserved: %#v", request.Thread)
	}
}

func TestCallerPreservesStartOnlyPartialEvidence(t *testing.T) {
	providerErr := errors.New("start failed after negotiation")
	run := codexsdk.StartedThreadRun{Start: protocolv2.ThreadStartResponse{Model: "effective-model"}}
	runner := &fakeRunner{result: run, err: providerErr}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	response, err := caller.Call(context.Background(), validRequest())
	if !errors.Is(err, providerErr) || response.Execution.EffectiveModel != "effective-model" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	details, ok := response.ProviderDetails.(Details)
	if !ok || details.Run.Start.Model != "effective-model" {
		t.Fatalf("details = %#v", response.ProviderDetails)
	}
}

func TestCallerPreservesPartialRunAndCause(t *testing.T) {
	providerErr := errors.New("turn failed")
	run := validStartedRun("", "gpt-start")
	run.Run.Turn.Status = protocolv2.TurnStatusFailed
	run.Run.FinalResponse = "partial"
	runner := &fakeRunner{result: run, err: providerErr}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	response, err := caller.Call(context.Background(), validRequest())
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider cause", err)
	}
	if response.FinalResponse != "partial" || response.Execution.ProviderName != "codex" {
		t.Fatalf("partial response = %#v", response)
	}
	details, ok := response.ProviderDetails.(Details)
	if !ok || details.Run.Start.Thread.ID == "" || details.Run.Run.Turn.Status != protocolv2.TurnStatusFailed {
		t.Fatalf("partial details = %#v", details)
	}
}

func TestCallDetailedAndStreamShareRequestConstruction(t *testing.T) {
	streamErr := errors.New("stream unavailable")
	runner := &fakeRunner{result: validStartedRun("ok", "gpt"), streamErr: streamErr}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	if _, err := caller.CallDetailed(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.CallStream(context.Background(), request); !errors.Is(err, streamErr) {
		t.Fatalf("CallStream error = %v", err)
	}
	if len(runner.requests) != 1 || len(runner.streamRequests) != 1 {
		t.Fatalf("run requests = %d stream requests = %d", len(runner.requests), len(runner.streamRequests))
	}
	left, _ := json.Marshal(runner.requests[0])
	right, _ := json.Marshal(runner.streamRequests[0])
	if !bytesEqual(left, right) {
		t.Fatalf("request construction differs:\n%s\n%s", left, right)
	}
}

func TestCallIsProjectionOfDetailedResult(t *testing.T) {
	run := validStartedRun("ok", "gpt")
	runner := &fakeRunner{result: run}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	detailed, err := caller.CallDetailed(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	want, err := responseFromRun(detailed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := caller.Call(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || got.Execution.Usage != nil {
		t.Fatalf("Call projection = %#v, want %#v", got, want)
	}
}

func TestCallerPublishesImmutableDetailsAndDefaults(t *testing.T) {
	run := validStartedRun("ok", "gpt")
	runner := &fakeRunner{result: run}
	roots := []string{"/one"}
	options := Options{Runner: runner, Defaults: codexsdk.StartThreadRunRequest{
		Thread: protocolv2.ThreadStartParams{RuntimeWorkspaceRoots: protocolv2.Value(roots)},
	}}
	caller, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	roots[0] = "/mutated"
	response, err := caller.Call(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	requestedRoots := runner.requests[0].Thread.RuntimeWorkspaceRoots
	if requestedRoots == nil || requestedRoots.Value == nil || (*requestedRoots.Value)[0] != "/one" {
		t.Fatalf("defaults aliased caller input: %#v", requestedRoots)
	}
	details := response.ProviderDetails.(Details)
	runner.result.Run.Notifications[0] = modelRerouted("mutated", "mutated")
	detailsAgain := response.ProviderDetails.(Details)
	rerouted, ok := detailsAgain.Run.Run.Notifications[0].AsModelRerouted()
	if !ok || rerouted.Params.FromModel != "gpt" || details.Run.Run.Notifications[0].Kind() != protocolv2.ServerNotificationKindModelRerouted {
		t.Fatal("details snapshot was aliased")
	}
}

func TestReadOnlyEphemeralProfileSetsAndVerifiesExactPolicy(t *testing.T) {
	run := validStartedRun("ok", "gpt")
	runner := &fakeRunner{result: run}
	options := ReadOnlyEphemeralOptions(runner)
	caller, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caller.CallDetailed(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	request := runner.requests[0]
	if request.Thread.Ephemeral == nil || request.Thread.Ephemeral.Value == nil || !*request.Thread.Ephemeral.Value {
		t.Fatalf("thread ephemeral = %#v", request.Thread.Ephemeral)
	}
	if request.Thread.Sandbox == nil || request.Thread.Sandbox.Value == nil || *request.Thread.Sandbox.Value != protocolv2.SandboxModeReadOnly {
		t.Fatalf("thread sandbox = %#v", request.Thread.Sandbox)
	}
	if request.Thread.ApprovalPolicy == nil || request.Thread.ApprovalPolicy.Value == nil || request.Thread.ApprovalPolicy.Value.Kind() != protocolv2.AskForApprovalKindNever {
		t.Fatalf("thread approval = %#v", request.Thread.ApprovalPolicy)
	}
	if request.Turn.SandboxPolicy == nil || request.Turn.SandboxPolicy.Value == nil || request.Turn.SandboxPolicy.Value.Kind() != protocolv2.SandboxPolicyKindReadOnly {
		t.Fatalf("turn sandbox = %#v", request.Turn.SandboxPolicy)
	}
	if request.Turn.ApprovalPolicy == nil || request.Turn.ApprovalPolicy.Value == nil || request.Turn.ApprovalPolicy.Value.Kind() != protocolv2.AskForApprovalKindNever {
		t.Fatalf("turn approval = %#v", request.Turn.ApprovalPolicy)
	}

}

func TestEffectiveProfileContractIsSharedByCallAndDetailed(t *testing.T) {
	type profileCase struct {
		name   string
		mutate func(*codexsdk.StartedThreadRun)
		want   string
	}
	cases := []profileCase{
		{name: "valid"},
		{name: "approval", mutate: func(run *codexsdk.StartedThreadRun) {
			run.Start.ApprovalPolicy = protocolv2.NewAskForApprovalOnRequest()
		}, want: "not never"},
		{name: "sandbox", mutate: func(run *codexsdk.StartedThreadRun) {
			run.Start.Sandbox = protocolv2.NewSandboxPolicyDangerFullAccess()
		}, want: "not read-only"},
		{name: "ephemeral", mutate: func(run *codexsdk.StartedThreadRun) {
			run.Start.Thread.Ephemeral = false
		}, want: "not ephemeral"},
	}
	paths := []struct {
		name string
		call func(*Caller, *fakeRunner) (codexsdk.StartedThreadRun, error)
	}{
		{name: "Call", call: func(caller *Caller, _ *fakeRunner) (codexsdk.StartedThreadRun, error) {
			response, err := caller.Call(context.Background(), validRequest())
			if details, ok := response.ProviderDetails.(Details); ok {
				return details.Run, err
			}
			return codexsdk.StartedThreadRun{}, err
		}},
		{name: "CallDetailed", call: func(caller *Caller, _ *fakeRunner) (codexsdk.StartedThreadRun, error) {
			return caller.CallDetailed(context.Background(), validRequest())
		}},
	}

	for _, testCase := range cases {
		for _, path := range paths {
			t.Run(testCase.name+"/"+path.name, func(t *testing.T) {
				run := validStartedRun("ok", "gpt")
				if testCase.mutate != nil {
					testCase.mutate(&run)
				}
				runner := &fakeRunner{result: run}
				caller, err := New(ReadOnlyEphemeralOptions(runner))
				if err != nil {
					t.Fatal(err)
				}
				got, err := path.call(caller, runner)
				if testCase.want == "" {
					if err != nil {
						t.Fatalf("call error = %v", err)
					}
				} else if !errors.Is(err, ErrEffectiveProfile) || !strings.Contains(err.Error(), testCase.want) {
					t.Fatalf("call error = %v, want ErrEffectiveProfile containing %q", err, testCase.want)
				}
				if !reflect.DeepEqual(got, run) {
					t.Fatalf("exact result = %#v, want %#v", got, run)
				}
			})
		}
	}
}

func TestCallDetailedValidatesDecodedMissingThreadIDApproval(t *testing.T) {
	partial := validStartedRun("", "decoded-model")
	partial.Start.Thread.ID = ""
	partial.Run = codexsdk.ThreadRunResult{}
	partial.Start.ApprovalPolicy = protocolv2.NewAskForApprovalOnRequest()
	runner := &fakeRunner{result: partial, err: codexsdk.ErrMissingThreadID}
	caller, err := New(ReadOnlyEphemeralOptions(runner))
	if err != nil {
		t.Fatal(err)
	}

	got, err := caller.CallDetailed(context.Background(), validRequest())
	if !errors.Is(err, codexsdk.ErrMissingThreadID) || !errors.Is(err, ErrEffectiveProfile) {
		t.Fatalf("CallDetailed error = %v, want missing-thread and profile causes", err)
	}
	if !strings.Contains(err.Error(), "not never") {
		t.Fatalf("CallDetailed error = %v, want approval mismatch", err)
	}
	if !reflect.DeepEqual(got, partial) {
		t.Fatalf("CallDetailed result = %#v, want exact partial evidence %#v", got, partial)
	}
}

func TestCallValidatesDecodedMissingThreadIDSandboxAndProjectsEvidence(t *testing.T) {
	partial := validStartedRun("", "decoded-model")
	partial.Start.Thread.ID = ""
	partial.Run = codexsdk.ThreadRunResult{}
	partial.Start.Sandbox = protocolv2.NewSandboxPolicyDangerFullAccess()
	roots := []string{"/decoded-root"}
	partial.Start.RuntimeWorkspaceRoots = &roots
	runner := &fakeRunner{result: partial, err: codexsdk.ErrMissingThreadID}
	caller, err := New(ReadOnlyEphemeralOptions(runner))
	if err != nil {
		t.Fatal(err)
	}

	response, err := caller.Call(context.Background(), validRequest())
	if !errors.Is(err, codexsdk.ErrMissingThreadID) || !errors.Is(err, ErrEffectiveProfile) {
		t.Fatalf("Call error = %v, want missing-thread and profile causes", err)
	}
	if !strings.Contains(err.Error(), "not read-only") {
		t.Fatalf("Call error = %v, want sandbox mismatch", err)
	}
	if response.Execution.ProviderName != "codex" || response.Execution.EffectiveModel != "decoded-model" {
		t.Fatalf("Call evidence = %#v, want decoded start projection", response.Execution)
	}
	details, ok := response.ProviderDetails.(Details)
	if !ok || !reflect.DeepEqual(details.Run, partial) {
		t.Fatalf("Call details = %#v, want exact partial evidence %#v", response.ProviderDetails, partial)
	}
	(*runner.result.Start.RuntimeWorkspaceRoots)[0] = "/mutated"
	gotRoots := details.Run.Start.RuntimeWorkspaceRoots
	if gotRoots == nil || (*gotRoots)[0] != "/decoded-root" {
		t.Fatalf("Call details alias runner result: %#v", gotRoots)
	}
}

func TestStreamValidatesDecodedMissingThreadIDEphemeralOnWaitAndErr(t *testing.T) {
	partial := validStartedRun("", "decoded-model")
	partial.Start.Thread.ID = ""
	partial.Start.Thread.Ephemeral = false
	partial.Run = codexsdk.ThreadRunResult{}
	missingIDErr := fmt.Errorf("malformed decoded start: %w", codexsdk.ErrMissingThreadID)
	runner := &fakeRunner{}
	caller, err := New(ReadOnlyEphemeralOptions(runner))
	if err != nil {
		t.Fatal(err)
	}
	stream := caller.wrapStream(&fakeStartedRunStream{result: partial, err: missingIDErr}, nil)

	got, waitErr := stream.Wait(context.Background())
	if !errors.Is(waitErr, codexsdk.ErrMissingThreadID) || !errors.Is(waitErr, ErrEffectiveProfile) {
		t.Fatalf("Wait error = %v, want missing-thread and profile causes", waitErr)
	}
	if !strings.Contains(waitErr.Error(), "not ephemeral") {
		t.Fatalf("Wait error = %v, want ephemeral mismatch", waitErr)
	}
	if !reflect.DeepEqual(got, partial) {
		t.Fatalf("Wait result = %#v, want exact partial evidence %#v", got, partial)
	}
	streamErr := stream.Err()
	if !errors.Is(streamErr, codexsdk.ErrMissingThreadID) || !errors.Is(streamErr, ErrEffectiveProfile) {
		t.Fatalf("Err = %v, want missing-thread and profile causes", streamErr)
	}
	if streamErr.Error() != waitErr.Error() {
		t.Fatalf("Err = %q, Wait error = %q, want stable terminal causes", streamErr, waitErr)
	}
}

func TestCallProjectsZeroValuedDecodedMissingThreadIDEvidence(t *testing.T) {
	runner := &fakeRunner{err: codexsdk.ErrMissingThreadID}
	caller, err := New(ReadOnlyEphemeralOptions(runner))
	if err != nil {
		t.Fatal(err)
	}

	response, err := caller.Call(context.Background(), validRequest())
	if !errors.Is(err, codexsdk.ErrMissingThreadID) || !errors.Is(err, ErrEffectiveProfile) {
		t.Fatalf("Call error = %v, want missing-thread and profile causes", err)
	}
	if response.Execution.ProviderName != "codex" {
		t.Fatalf("Call evidence = %#v, want decoded start provider projection", response.Execution)
	}
	details, ok := response.ProviderDetails.(Details)
	if !ok || !reflect.DeepEqual(details.Run, codexsdk.StartedThreadRun{}) {
		t.Fatalf("Call details = %#v, want typed zero-valued decoded evidence", response.ProviderDetails)
	}
}

func TestCallDoesNotSynthesizeProfileMismatchBeforeStartResponse(t *testing.T) {
	transportErr := errors.New("thread/start transport failed")
	runner := &fakeRunner{err: transportErr}
	caller, err := New(ReadOnlyEphemeralOptions(runner))
	if err != nil {
		t.Fatal(err)
	}

	response, err := caller.Call(context.Background(), validRequest())
	if !errors.Is(err, transportErr) || errors.Is(err, ErrEffectiveProfile) {
		t.Fatalf("Call error = %v, want transport cause without profile mismatch", err)
	}
	if !reflect.DeepEqual(response, llmadapter.Response{}) {
		t.Fatalf("Call response = %#v, want no synthetic evidence", response)
	}
}

func TestStreamJoinsProviderAndProfileErrorsWithoutLosingExactEvidence(t *testing.T) {
	providerErr := &typedProviderError{code: "quota"}
	run := validStartedRun("partial", "gpt")
	run.Start.ApprovalPolicy = protocolv2.NewAskForApprovalOnRequest()
	run.Start.Sandbox = protocolv2.NewSandboxPolicyDangerFullAccess()
	run.Start.Thread.Ephemeral = false
	run.Run.Diagnostics = []codexsdk.DiagnosticRef{{Kind: "provider", Path: "turn/completed"}}
	run.Run.Notifications = []protocolv2.ServerNotification{modelRerouted("gpt", "gpt-rerouted")}
	run.Run.Usage = &protocolv2.ThreadTokenUsage{Total: protocolv2.TokenUsageBreakdown{InputTokens: 3, OutputTokens: 5}}

	runner := &fakeRunner{result: run, err: providerErr}
	caller, err := New(ReadOnlyEphemeralOptions(runner))
	if err != nil {
		t.Fatal(err)
	}
	inner := &fakeStartedRunStream{result: run, err: providerErr, notifications: run.Run.Notifications}
	stream := caller.wrapStream(inner, nil)
	got, err := stream.Wait(context.Background())
	if !errors.Is(err, providerErr) || !errors.Is(err, ErrEffectiveProfile) {
		t.Fatalf("Wait error = %v, want provider and profile causes", err)
	}
	var typedWaitErr *typedProviderError
	if !errors.As(err, &typedWaitErr) || typedWaitErr.code != "quota" {
		t.Fatalf("Wait error = %v, want typed provider cause", err)
	}
	if !reflect.DeepEqual(got, run) {
		t.Fatalf("Wait result = %#v, want complete exact evidence %#v", got, run)
	}
	streamErr := stream.Err()
	if !errors.Is(streamErr, providerErr) || !errors.Is(streamErr, ErrEffectiveProfile) {
		t.Fatalf("Err = %v, want provider and profile causes", streamErr)
	}
	var typedStreamErr *typedProviderError
	if !errors.As(streamErr, &typedStreamErr) || typedStreamErr.code != "quota" {
		t.Fatalf("Err = %v, want typed provider cause", streamErr)
	}
	if !stream.Next(context.Background()) || !reflect.DeepEqual(stream.Notification(), run.Run.Notifications[0]) {
		t.Fatalf("notification delegation lost exact fact: %#v", stream.Notification())
	}
	if err := stream.Close(); err != nil || !inner.closed {
		t.Fatalf("Close = %v, closed=%v", err, inner.closed)
	}

	activeRun := run
	activeRun.Run.Turn.Status = protocolv2.TurnStatusInProgress
	active := caller.wrapStream(&fakeStartedRunStream{result: activeRun}, nil)
	if err := active.Err(); err != nil {
		t.Fatalf("active stream Err = %v, want nil until terminal", err)
	}
}

func TestReadOnlyEphemeralProfileRejectsConflictingDefaults(t *testing.T) {
	runner := &fakeRunner{}
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{
			name: "thread sandbox",
			mutate: func(options *Options) {
				options.Defaults.Thread.Sandbox = protocolv2.Value(protocolv2.SandboxModeDangerFullAccess)
			},
		},
		{
			name: "thread approval",
			mutate: func(options *Options) {
				options.Defaults.Thread.ApprovalPolicy = protocolv2.Value(protocolv2.NewAskForApprovalOnRequest())
			},
		},
		{
			name: "thread ephemeral",
			mutate: func(options *Options) {
				options.Defaults.Thread.Ephemeral = protocolv2.Value(false)
			},
		},
		{
			name: "turn sandbox",
			mutate: func(options *Options) {
				options.Defaults.Turn.SandboxPolicy = protocolv2.Value(protocolv2.NewSandboxPolicyWorkspaceWrite(protocolv2.SandboxPolicyWorkspaceWrite{}))
			},
		},
		{
			name: "turn approval",
			mutate: func(options *Options) {
				options.Defaults.Turn.ApprovalPolicy = protocolv2.Value(protocolv2.NewAskForApprovalOnRequest())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := ReadOnlyEphemeralOptions(runner)
			test.mutate(&options)
			if _, err := New(options); err == nil {
				t.Fatal("New accepted conflicting profile default")
			}
			if len(runner.requests) != 0 || len(runner.streamRequests) != 0 {
				t.Fatalf("runner invoked: starts=%d streams=%d", len(runner.requests), len(runner.streamRequests))
			}
		})
	}
}

func TestReadOnlyEphemeralProfileNormalizesUnsetDefaultsAndPreservesExactDefaults(t *testing.T) {
	runner := &fakeRunner{result: validStartedRun("ok", "gpt")}
	options := ReadOnlyEphemeralOptions(runner)
	options.Defaults.Thread.Ephemeral = nil
	options.Defaults.Thread.Sandbox = nil
	options.Defaults.Thread.ApprovalPolicy = nil
	options.Defaults.Turn.SandboxPolicy = nil
	options.Defaults.Turn.ApprovalPolicy = nil
	options.Defaults.Thread.Model = protocolv2.Value("gpt-request")
	options.Defaults.Thread.CWD = protocolv2.Value("/workspace/project")
	options.Defaults.Thread.ServiceTier = protocolv2.Value("flex")
	options.Defaults.Thread.RuntimeWorkspaceRoots = protocolv2.Value([]string{"/workspace"})
	options.Defaults.Turn.Effort = protocolv2.Value(protocolv2.ReasoningEffort("high"))

	caller, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caller.CallDetailed(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	request := runner.requests[0]
	assertReadOnlyEphemeralRequest(t, request)
	if request.Thread.Model == nil || request.Thread.Model.Value == nil || *request.Thread.Model.Value != "gpt-request" ||
		request.Thread.CWD == nil || request.Thread.CWD.Value == nil || *request.Thread.CWD.Value != "/workspace/project" ||
		request.Thread.ServiceTier == nil || request.Thread.ServiceTier.Value == nil || *request.Thread.ServiceTier.Value != "flex" ||
		request.Thread.RuntimeWorkspaceRoots == nil || request.Thread.RuntimeWorkspaceRoots.Value == nil || !reflect.DeepEqual(*request.Thread.RuntimeWorkspaceRoots.Value, []string{"/workspace"}) ||
		request.Turn.Effort == nil || request.Turn.Effort.Value == nil || *request.Turn.Effort.Value != protocolv2.ReasoningEffort("high") {
		t.Fatalf("non-profile exact defaults changed: %#v %#v", request.Thread, request.Turn)
	}
}

func TestReadOnlyEphemeralProfileReappliesSafeRequestOnEveryCallPath(t *testing.T) {
	tests := []struct {
		name string
		call func(*Caller) error
	}{
		{name: "Call", call: func(caller *Caller) error {
			_, err := caller.Call(context.Background(), validRequest())
			return err
		}},
		{name: "CallDetailed", call: func(caller *Caller) error {
			_, err := caller.CallDetailed(context.Background(), validRequest())
			return err
		}},
		{name: "CallStream", call: func(caller *Caller) error {
			_, err := caller.CallStream(context.Background(), validRequest())
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{result: validStartedRun("unsafe", "gpt")}
			caller, err := New(ReadOnlyEphemeralOptions(runner))
			if err != nil {
				t.Fatal(err)
			}
			caller.defaults.Thread.Ephemeral = protocolv2.Value(false)
			caller.defaults.Thread.Sandbox = protocolv2.Value(protocolv2.SandboxModeDangerFullAccess)
			caller.defaults.Thread.ApprovalPolicy = protocolv2.Value(protocolv2.NewAskForApprovalOnRequest())
			caller.defaults.Turn.SandboxPolicy = protocolv2.Value(protocolv2.NewSandboxPolicyWorkspaceWrite(protocolv2.SandboxPolicyWorkspaceWrite{}))
			caller.defaults.Turn.ApprovalPolicy = protocolv2.Value(protocolv2.NewAskForApprovalOnRequest())
			if err := test.call(caller); err != nil {
				t.Fatalf("call failed after request profile reapplication: %v", err)
			}
			if len(runner.requests)+len(runner.streamRequests) != 1 {
				t.Fatalf("runner invocations: starts=%d streams=%d", len(runner.requests), len(runner.streamRequests))
			}
			if len(runner.requests) == 1 {
				assertReadOnlyEphemeralRequest(t, runner.requests[0])
			} else {
				assertReadOnlyEphemeralRequest(t, runner.streamRequests[0])
			}
		})
	}
}

func assertReadOnlyEphemeralRequest(t *testing.T, request codexsdk.StartThreadRunRequest) {
	t.Helper()
	if request.Thread.Ephemeral == nil || request.Thread.Ephemeral.Value == nil || !*request.Thread.Ephemeral.Value {
		t.Fatalf("thread ephemeral = %#v", request.Thread.Ephemeral)
	}
	if request.Thread.Sandbox == nil || request.Thread.Sandbox.Value == nil || *request.Thread.Sandbox.Value != protocolv2.SandboxModeReadOnly {
		t.Fatalf("thread sandbox = %#v", request.Thread.Sandbox)
	}
	if request.Thread.ApprovalPolicy == nil || request.Thread.ApprovalPolicy.Value == nil || request.Thread.ApprovalPolicy.Value.Kind() != protocolv2.AskForApprovalKindNever {
		t.Fatalf("thread approval = %#v", request.Thread.ApprovalPolicy)
	}
	if request.Turn.SandboxPolicy == nil || request.Turn.SandboxPolicy.Value == nil || request.Turn.SandboxPolicy.Value.Kind() != protocolv2.SandboxPolicyKindReadOnly {
		t.Fatalf("turn sandbox = %#v", request.Turn.SandboxPolicy)
	}
	if request.Turn.ApprovalPolicy == nil || request.Turn.ApprovalPolicy.Value == nil || request.Turn.ApprovalPolicy.Value.Kind() != protocolv2.AskForApprovalKindNever {
		t.Fatalf("turn approval = %#v", request.Turn.ApprovalPolicy)
	}
}

func TestCallerWorksThroughLLMAdapterDetailedPath(t *testing.T) {
	runner := &fakeRunner{result: validStartedRun(`{"answer":true}`, "gpt")}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	result, err := llmadapter.ValueDetailed[map[string]bool](context.Background(), caller, "answer")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Value["answer"] || result.Response.Execution.ProviderName != "codex" {
		t.Fatalf("result = %#v", result)
	}
}

func TestStrictOutputSchemaCompatibilityMatrix(t *testing.T) {
	t.Run("required-scalar-preserved", func(t *testing.T) {
		type output struct {
			Name string `json:"name"`
		}
		assertSchemaJSONValueEqual(t, schemaFor[output](t))
	})
	t.Run("optional-pointer-preserved", func(t *testing.T) {
		type output struct {
			Name string  `json:"name"`
			Note *string `json:"note,omitempty"`
		}
		assertGoSchemaAccepted[output](t)
		assertDecodedValuesEqual[output](t, `{"name":"x"}`, `{"name":"x","note":null}`)
	})
	t.Run("optional-scalar-fails-closed", func(t *testing.T) {
		type output struct {
			Name  string `json:"name"`
			Score int    `json:"score,omitempty"`
		}
		assertSchemaError(t, schemaFor[output](t), "optional_non_nullable", "/properties/score")
	})
	t.Run("nested-optional-pointer-preserved", func(t *testing.T) {
		type child struct {
			Note *string `json:"note,omitempty"`
		}
		type output struct {
			Child child `json:"child"`
		}
		assertGoSchemaAccepted[output](t)
		assertDecodedValuesEqual[output](t, `{"child":{}}`, `{"child":{"note":null}}`)
	})
	t.Run("optional-map-fails-closed", func(t *testing.T) {
		type output struct {
			Labels map[string]int `json:"labels,omitempty"`
		}
		assertSchemaError(t, schemaFor[output](t), "optional_non_nullable", "/properties/labels")
	})
	t.Run("optional-slice-preserved", func(t *testing.T) {
		type output struct {
			Items []string `json:"items,omitempty"`
		}
		assertGoSchemaAccepted[output](t)
		assertDecodedValuesEqual[output](t, `{}`, `{"items":null}`)
	})
	t.Run("optional-pointer-to-slice-preserved", func(t *testing.T) {
		type output struct {
			Items *[]string `json:"items,omitempty"`
		}
		assertGoSchemaAccepted[output](t)
		assertDecodedValuesEqual[output](t, `{}`, `{"items":null}`)
	})
	t.Run("optional-raw-message-has-decoding-limitation", func(t *testing.T) {
		type output struct {
			Payload json.RawMessage `json:"payload,omitempty"`
		}
		assertGoSchemaAccepted[output](t)

		var absent, explicitNull output
		if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(`{"payload":null}`), &explicitNull); err != nil {
			t.Fatal(err)
		}
		if absent.Payload != nil || string(explicitNull.Payload) != "null" {
			t.Fatalf("RawMessage absence/null distinction changed: absent=%q null=%q", absent.Payload, explicitNull.Payload)
		}
	})
	t.Run("custom-unmarshaler-has-decoding-limitation", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"object","properties":{"value":{"type":["object","null"]}}}`)
		if _, err := StrictOutputSchemaFromJSON(raw); err != nil {
			t.Fatal(err)
		}

		type output struct {
			Value nullAwareString `json:"value,omitempty"`
		}
		var absent, explicitNull output
		if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(`{"value":null}`), &explicitNull); err != nil {
			t.Fatal(err)
		}
		if absent.Value.SawNull || !explicitNull.Value.SawNull {
			t.Fatal("custom unmarshaler did not demonstrate the documented absence/null distinction")
		}
	})
	t.Run("local-ref-preserved", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"object","properties":{"note":{"$ref":"#/$defs/note"}},"$defs":{"note":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`)
		schema, err := StrictOutputSchemaFromJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := schema.MarshalJSON()
		if !strings.Contains(string(encoded), `"required":["note"]`) ||
			!strings.Contains(string(encoded), `"$ref":"#/$defs/note"`) ||
			!strings.Contains(string(encoded), `"anyOf":[{"type":"string"},{"type":"null"}]`) {
			t.Fatalf("schema = %s", encoded)
		}
	})
	t.Run("nested-ref-with-sibling-constraint-fails-closed", func(t *testing.T) {
		assertSchemaError(t,
			json.RawMessage(`{"type":"object","properties":{"value":{"$ref":"#/$defs/outer","type":["string","null"]}},"$defs":{"outer":{"$ref":"#/$defs/inner","type":["string","null"]},"inner":{"type":"string"}}}`),
			"optional_non_nullable", "/properties/value")
	})
	t.Run("boolean-schema-has-codex-limitation", func(t *testing.T) {
		for _, raw := range []json.RawMessage{json.RawMessage(`true`), json.RawMessage(`false`)} {
			assertSchemaJSONValueEqual(t, raw)
		}
	})
	t.Run("draft-2020-12-preserved", func(t *testing.T) {
		raw := json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["note"],"properties":{"note":{"type":["string","null"],"minLength":2}}}`)
		schema, err := StrictOutputSchemaFromJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := schema.MarshalJSON()
		if !strings.Contains(string(encoded), `"$schema":"https://json-schema.org/draft/2020-12/schema"`) ||
			!strings.Contains(string(encoded), `"minLength":2`) {
			t.Fatalf("draft or constraint changed: %s", encoded)
		}
	})
	t.Run("draft-7-ref-sibling-limitation", func(t *testing.T) {
		_, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"value":{"$ref":"#/$defs/nullable","type":"string"}},"$defs":{"nullable":{"type":["string","null"]}}}`))
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unsupported-draft-fails-closed", func(t *testing.T) {
		assertSchemaError(t, json.RawMessage(`{"$schema":"https://json-schema.org/draft/9999/schema","type":"object"}`), "invalid_schema", "")
	})
	t.Run("unknown-annotation-preserved", func(t *testing.T) {
		schema, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","x-note":{"level":2}}}}`))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := schema.MarshalJSON()
		if !strings.Contains(string(encoded), `"x-note":{"level":2}`) {
			t.Fatalf("unknown keyword changed: %s", encoded)
		}
	})
	t.Run("unknown-assertion-has-validation-limitation", func(t *testing.T) {
		schema, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","x-must-equal":"fixed"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := schema.MarshalJSON()
		if !strings.Contains(string(encoded), `"x-must-equal":"fixed"`) {
			t.Fatalf("unknown assertion changed: %s", encoded)
		}
	})
	t.Run("dynamic-anchor-has-resolution-limitation", func(t *testing.T) {
		schema, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"$dynamicAnchor":"node","type":"object"}`))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := schema.MarshalJSON()
		if !strings.Contains(string(encoded), `"$dynamicAnchor":"node"`) {
			t.Fatalf("dynamic anchor changed: %s", encoded)
		}
	})
	t.Run("vocabulary-fails-closed", func(t *testing.T) {
		assertSchemaError(t,
			json.RawMessage(`{"$vocabulary":{"https://example.test/vocab":true},"type":"object"}`),
			"invalid_schema", "")
	})
	t.Run("additional-properties-schema-preserved", func(t *testing.T) {
		schema, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"type":"object","additionalProperties":{"type":"object","maxProperties":2,"x-note":{"level":3},"properties":{"note":{"type":["string","null"]}}}}`))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := schema.MarshalJSON()
		if !strings.Contains(string(encoded), `"required":["note"]`) ||
			!strings.Contains(string(encoded), `"maxProperties":2`) ||
			!strings.Contains(string(encoded), `"x-note":{"level":3}`) {
			t.Fatalf("nested additionalProperties schema was not normalized: %s", encoded)
		}
	})
	t.Run("conditional-null-fails-closed", func(t *testing.T) {
		assertSchemaError(t,
			json.RawMessage(`{"type":"object","properties":{"value":{"if":{"type":"null"},"then":false,"else":true}}}`),
			"optional_non_nullable", "/properties/value")
	})
	t.Run("cyclic-ref-fails-closed", func(t *testing.T) {
		assertSchemaError(t, json.RawMessage(`{"$defs":{"node":{"$ref":"#/$defs/node"}},"$ref":"#/$defs/node"}`), "cyclic_ref", "/$defs/node/$ref")
	})
	t.Run("external-ref-fails-closed", func(t *testing.T) {
		assertSchemaError(t, json.RawMessage(`{"$ref":"https://example.test/schema"}`), "external_ref", "/$ref")
	})
	t.Run("unresolvable-ref-fails-closed", func(t *testing.T) {
		assertSchemaError(t, json.RawMessage(`{"$ref":"#/$defs/missing"}`), "unresolvable_ref", "/$ref")
	})
	t.Run("dynamic-ref-fails-closed", func(t *testing.T) {
		assertSchemaError(t, json.RawMessage(`{"$dynamicRef":"#node"}`), "unsupported_dynamic_ref", "/$dynamicRef")
	})
}

func TestStrictOutputSchemaUsesJSONSchemaSemanticsForNullAdmission(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		wantKind string
	}{
		{
			name:     "reference rejects null while sibling admits it",
			schema:   `{"type":"object","properties":{"x":{"$ref":"#/$defs/nonNull","type":["string","null"]}},"$defs":{"nonNull":{"type":"string"}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:     "reference admits null while sibling rejects it",
			schema:   `{"type":"object","properties":{"x":{"$ref":"#/$defs/nullable","type":"string"}},"$defs":{"nullable":{"type":["string","null"]}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:   "reference and sibling both admit null",
			schema: `{"type":"object","properties":{"x":{"$ref":"#/$defs/nullable","type":["string","null"]}},"$defs":{"nullable":{"type":["string","null"]}}}`,
		},
		{
			name:     "nested local references with siblings",
			schema:   `{"type":"object","properties":{"x":{"$ref":"#/$defs/outer","type":["string","null"]}},"$defs":{"outer":{"$ref":"#/$defs/inner","type":["string","null"]},"inner":{"type":"string"}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:     "allOf requires every branch to admit null",
			schema:   `{"type":"object","properties":{"x":{"allOf":[{"type":["string","null"]},{"type":"string"}]}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:   "anyOf accepts one matching branch",
			schema: `{"type":"object","properties":{"x":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
		},
		{
			name:     "oneOf rejects two matching branches",
			schema:   `{"type":"object","properties":{"x":{"oneOf":[{}, {"type":"null"}]}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:     "not rejects null",
			schema:   `{"type":"object","properties":{"x":{"not":{"const":null}}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:   "enum accepts null",
			schema: `{"type":"object","properties":{"x":{"enum":[null,"x"]}}}`,
		},
		{
			name:     "conditional applies matching then branch",
			schema:   `{"type":"object","properties":{"x":{"if":{"type":"null"},"then":{"const":"not-null"},"else":true}}}`,
			wantKind: "optional_non_nullable",
		},
		{
			name:   "draft seven ignores reference siblings",
			schema: `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"x":{"$ref":"#/$defs/nullable","type":"string"}},"$defs":{"nullable":{"type":["string","null"]}}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := StrictOutputSchemaFromJSON(json.RawMessage(test.schema))
			if test.wantKind != "" {
				var policyErr *SchemaPolicyError
				if !errors.As(err, &policyErr) || policyErr.Kind != test.wantKind || policyErr.Path != "/properties/x" {
					t.Fatalf("error = %#v, want %s at /properties/x", policyErr, test.wantKind)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := schema.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"required":["x"]`) {
				t.Fatalf("schema = %s", encoded)
			}
		})
	}
}

func TestStrictOutputSchemaFailsClosedWhenNullProbeSchemaDoesNotCompile(t *testing.T) {
	assertSchemaErrorKind(t, json.RawMessage(`{"type":"object","properties":{"x":{"type":["null",1]}}}`), "nullable_analysis")
	assertSchemaErrorKind(t, json.RawMessage(`{"type":"object","required":["x"],"properties":{"x":{"type":["null",1]}}}`), "invalid_schema")
}

func TestStrictOutputSchemaDecisionMatchesDirectValidator(t *testing.T) {
	propertySchemas := []string{
		`{"type":"null"}`,
		`{"type":"string"}`,
		`{"allOf":[{"type":["string","null"]},{"const":null}]}`,
		`{"anyOf":[{"type":"string"},{"enum":[null]}]}`,
		`{"oneOf":[{}, {"type":"null"}]}`,
		`{"not":{"enum":[null]}}`,
		`{"if":{"type":"null"},"then":false,"else":true}`,
	}

	for _, propertySchema := range propertySchemas {
		raw := json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"x":` + propertySchema + `}}`)
		var document any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("https://test.invalid/schema.json", document); err != nil {
			t.Fatal(err)
		}
		property, err := compiler.Compile("https://test.invalid/schema.json#/properties/x")
		if err != nil {
			t.Fatal(err)
		}
		wantPromotion := property.Validate(nil) == nil
		_, transformErr := StrictOutputSchemaFromJSON(raw)
		gotPromotion := transformErr == nil
		if gotPromotion != wantPromotion {
			t.Errorf("property %s: promoted = %v, direct validator accepts null = %v, error = %v", propertySchema, gotPromotion, wantPromotion, transformErr)
		}
	}
}

func TestCallerRejectsUncertainNullAdmissionBeforeRunnerInvocation(t *testing.T) {
	runner := &fakeRunner{}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	_, err = caller.CallDetailed(context.Background(), llmadapter.Request{
		Prompt:       "must not run",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":["null",1]}}}`),
	})
	var policyErr *SchemaPolicyError
	if !errors.As(err, &policyErr) || policyErr.Kind != "nullable_analysis" || policyErr.Path != "/properties/x" {
		t.Fatalf("error = %#v", policyErr)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner requests = %d", len(runner.requests))
	}
}

func TestStrictOutputSchemaRejectsDuplicateKeysAndPreservesPointerPath(t *testing.T) {
	assertSchemaErrorKind(t, json.RawMessage(`{"type":"object","type":"string"}`), "invalid_json")
	_, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"type":"object","properties":{"a/b~c":{"type":"string"}}}`))
	var policyErr *SchemaPolicyError
	if !errors.As(err, &policyErr) || policyErr.Path != "/properties/a~1b~0c" {
		t.Fatalf("error = %#v", policyErr)
	}
}

func TestStrictOutputSchemaTraversesSupportedSubschemaPositions(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind string
	}{
		{"additional items", `{"additionalItems":{"type":"object","properties":{"value":{"type":"string"}}}}`, "optional_non_nullable"},
		{"content schema", `{"contentSchema":{"type":"object","properties":{"value":{"type":"string"}}}}`, "optional_non_nullable"},
		{"tuple items", `{"items":[{"type":"object","properties":{"value":{"type":"string"}}}]}`, "optional_non_nullable"},
		{"schema dependency", `{"dependencies":{"value":{"type":"object","properties":{"nested":{"type":"string"}}}}}`, "optional_non_nullable"},
		{"dynamic ref", `{"$dynamicRef":"#node"}`, "unsupported_dynamic_ref"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertSchemaErrorKind(t, json.RawMessage(test.raw), test.kind)
		})
	}

	if _, err := StrictOutputSchemaFromJSON(json.RawMessage(`{"dependencies":{"value":["other"]}}`)); err != nil {
		t.Fatalf("property dependency rejected: %v", err)
	}
}

func assertGoSchemaAccepted[T any](t *testing.T) {
	t.Helper()
	if _, err := StrictOutputSchemaFromJSON(schemaFor[T](t)); err != nil {
		t.Fatalf("schema rejected: %v\n%s", err, schemaFor[T](t))
	}
}

func assertDecodedValuesEqual[T any](t *testing.T, absentJSON, nullJSON string) {
	t.Helper()
	var absent, explicitNull T
	if err := json.Unmarshal([]byte(absentJSON), &absent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(nullJSON), &explicitNull); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(absent, explicitNull) {
		t.Fatalf("absence/null decoded values differ: absent=%#v null=%#v", absent, explicitNull)
	}
}

func assertSchemaJSONValueEqual(t *testing.T, raw json.RawMessage) {
	t.Helper()
	schema, err := StrictOutputSchemaFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := schema.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("schema JSON value changed:\nbefore: %s\nafter:  %s", raw, encoded)
	}
}

func schemaFor[T any](t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := llmschema.SchemaJSONFor[T]()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertSchemaErrorKind(t *testing.T, raw json.RawMessage, kind string) {
	t.Helper()
	_, err := StrictOutputSchemaFromJSON(raw)
	var policyErr *SchemaPolicyError
	if !errors.As(err, &policyErr) || policyErr.Kind != kind {
		t.Fatalf("error = %v, want SchemaPolicyError kind %s", err, kind)
	}
}

func assertSchemaError(t *testing.T, raw json.RawMessage, kind, path string) {
	t.Helper()
	_, err := StrictOutputSchemaFromJSON(raw)
	var policyErr *SchemaPolicyError
	if !errors.As(err, &policyErr) || policyErr.Kind != kind || policyErr.Path != path {
		t.Fatalf("error = %#v, want SchemaPolicyError kind %s at %q", policyErr, kind, path)
	}

	runner := &fakeRunner{}
	caller, err := New(Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	_, err = caller.CallDetailed(context.Background(), llmadapter.Request{Prompt: "must not run", OutputSchema: raw})
	policyErr = nil
	if !errors.As(err, &policyErr) || policyErr.Kind != kind || policyErr.Path != path {
		t.Fatalf("Caller error = %#v, want SchemaPolicyError kind %s at %q", policyErr, kind, path)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner received %d requests after fail-closed schema error", len(runner.requests))
	}
}

func validRequest() llmadapter.Request {
	return llmadapter.Request{
		Prompt:       "prompt",
		OutputSchema: json.RawMessage(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`),
	}
}

func validStartedRun(final, model string) codexsdk.StartedThreadRun {
	phase := protocolv2.Value(protocolv2.MessagePhaseFinalAnswer)
	items := []protocolv2.ThreadItem{}
	if final != "" {
		items = append(items, protocolv2.NewThreadItemAgentMessage(protocolv2.ThreadItemAgentMessage{
			ID: "item-1", Text: final, Phase: phase,
		}))
	}
	return codexsdk.StartedThreadRun{
		Start: protocolv2.ThreadStartResponse{
			ApprovalPolicy:    protocolv2.NewAskForApprovalNever(),
			ApprovalsReviewer: protocolv2.ApprovalsReviewerUser,
			CWD:               "/workspace",
			Model:             model,
			ModelProvider:     "openai",
			Sandbox:           protocolv2.NewSandboxPolicyReadOnly(protocolv2.SandboxPolicyReadOnly{}),
			Thread: protocolv2.Thread{
				CliVersion: "test", CWD: "/workspace", Ephemeral: true, ID: "thread-1",
				ModelProvider: "openai", Preview: "preview", SessionID: "session-1",
				Source: protocolv2.NewSessionSourceAppServer(), Status: protocolv2.NewThreadStatusIdle(),
				Turns: []protocolv2.Turn{},
			},
		},
		Run: codexsdk.ThreadRunResult{
			Turn:          protocolv2.Turn{ID: "turn-1", Items: items, Status: protocolv2.TurnStatusCompleted},
			FinalResponse: final,
			Notifications: []protocolv2.ServerNotification{modelRerouted(model, model)},
		},
	}
}

func modelRerouted(from, to string) protocolv2.ServerNotification {
	return protocolv2.NewServerNotificationModelRerouted(protocolv2.ServerNotificationModelRerouted{
		Params: protocolv2.ModelReroutedNotification{
			FromModel: from, ToModel: to, Reason: protocolv2.ModelRerouteReasonHighRiskCyberActivity, ThreadID: "thread-1", TurnID: "turn-1",
		},
	})
}

func outputSchemaPointer(t *testing.T, raw string) *protocolv2.OutputSchema {
	t.Helper()
	schema, err := protocolv2.OutputSchemaFromJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return &schema
}

func bytesEqual(left, right []byte) bool { return string(left) == string(right) }
