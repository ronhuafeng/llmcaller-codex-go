package codexcaller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)

var (
	ErrNilThreadClient   = errors.New("llmcaller/codex: thread client is nil")
	ErrMissingSchemaJSON = errors.New("llmcaller/codex: output schema JSON is required")
)

type ThreadStarter interface {
	StartThread(ctx context.Context, req codexsdk.StartThreadRequest) (codexsdk.ThreadRunResult, error)
}

type Options struct {
	ThreadClient      ThreadStarter
	Model             string
	CWD               string
	Effort            codexsdk.ReasoningEffort
	ApprovalPolicy    codexsdk.ApprovalPolicy
	ApprovalsReviewer codexsdk.ApprovalsReviewer
	Ephemeral         *bool
}

type Caller struct {
	threadClient      ThreadStarter
	model             string
	cwd               string
	effort            codexsdk.ReasoningEffort
	approvalPolicy    codexsdk.ApprovalPolicy
	approvalsReviewer codexsdk.ApprovalsReviewer
	ephemeral         *bool
}

var _ llmadapter.Caller = (*Caller)(nil)

func New(options Options) *Caller {
	return &Caller{
		threadClient:      options.ThreadClient,
		model:             options.Model,
		cwd:               options.CWD,
		effort:            options.Effort,
		approvalPolicy:    options.ApprovalPolicy,
		approvalsReviewer: options.ApprovalsReviewer,
		ephemeral:         options.Ephemeral,
	}
}

func (caller *Caller) Call(ctx context.Context, request llmadapter.Request) (llmadapter.Response, error) {
	if caller == nil || caller.threadClient == nil {
		return llmadapter.Response{}, ErrNilThreadClient
	}
	outputSchema, err := StrictOutputSchemaFromJSON(request.OutputSchema)
	if err != nil {
		return llmadapter.Response{}, err
	}
	result, err := caller.threadClient.StartThread(ctx, codexsdk.StartThreadRequest{
		Input:             codexsdk.Text(request.Prompt),
		OutputSchema:      outputSchema,
		Ephemeral:         caller.ephemeral,
		Model:             caller.model,
		CWD:               caller.cwd,
		Effort:            caller.effort,
		ApprovalPolicy:    caller.approvalPolicy,
		ApprovalsReviewer: caller.approvalsReviewer,
	})
	if err != nil {
		return llmadapter.Response{}, err
	}
	return llmadapter.Response{FinalResponse: result.FinalResponse}, nil
}

func StrictOutputSchemaFromJSON(raw json.RawMessage) (protocolv2.OutputSchema, error) {
	if len(raw) == 0 {
		return protocolv2.OutputSchema{}, ErrMissingSchemaJSON
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return protocolv2.OutputSchema{}, fmt.Errorf("parse Codex output schema JSON: %w", err)
	}
	requireAllObjectProperties(value)
	out, err := json.Marshal(value)
	if err != nil {
		return protocolv2.OutputSchema{}, fmt.Errorf("marshal Codex output schema JSON: %w", err)
	}
	schema, err := protocolv2.OutputSchemaFromJSON(out)
	if err != nil {
		return protocolv2.OutputSchema{}, fmt.Errorf("parse Codex output schema: %w", err)
	}
	return schema, nil
}

func requireAllObjectProperties(value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if properties, ok := object["properties"].(map[string]any); ok && len(properties) != 0 {
		required := make([]string, 0, len(properties))
		for key, property := range properties {
			required = append(required, key)
			requireAllObjectProperties(property)
		}
		sort.Strings(required)
		object["required"] = required
	}
	if items, ok := object["items"]; ok {
		requireAllObjectProperties(items)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		list, ok := object[key].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			requireAllObjectProperties(item)
		}
	}
}
