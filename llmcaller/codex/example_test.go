package codexcaller_test

import (
	"context"
	"fmt"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2"
	codexcaller "github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)

type exampleRunner struct{}

func (exampleRunner) Start(context.Context, codexsdk.StartThreadRunRequest) (codexsdk.StartedThreadRun, error) {
	return codexsdk.StartedThreadRun{
		Start: protocolv2.ThreadStartResponse{
			ApprovalPolicy:    protocolv2.NewAskForApprovalNever(),
			ApprovalsReviewer: protocolv2.ApprovalsReviewerUser,
			Model:             "gpt-example",
			Sandbox:           protocolv2.NewSandboxPolicyReadOnly(protocolv2.SandboxPolicyReadOnly{}),
			Thread: protocolv2.Thread{
				ID: "thread-example", Ephemeral: true,
				Source: protocolv2.NewSessionSourceAppServer(),
				Status: protocolv2.NewThreadStatusIdle(),
				Turns:  []protocolv2.Turn{},
			},
		},
		Run: codexsdk.ThreadRunResult{
			Turn:          protocolv2.Turn{ID: "turn-example", Items: []protocolv2.ThreadItem{}, Status: protocolv2.TurnStatusCompleted},
			FinalResponse: `{"answer":"three layers"}`,
		},
	}, nil
}

func (exampleRunner) StartStream(context.Context, codexsdk.StartThreadRunRequest) (*codexsdk.Stream[codexsdk.StartedThreadRun], error) {
	return nil, nil
}

func Example() {
	caller, err := codexcaller.New(codexcaller.ReadOnlyEphemeralOptions(exampleRunner{}))
	if err != nil {
		panic(err)
	}
	type result struct {
		Answer string `json:"answer"`
	}
	value, err := llmadapter.Value[result](context.Background(), caller, "Describe the layering.")
	if err != nil {
		panic(err)
	}
	fmt.Println(value.Answer)
	// Output: three layers
}
