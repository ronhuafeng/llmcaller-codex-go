package codexcaller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	// ErrNilThreadRunner reports a missing or typed-nil runner.
	ErrNilThreadRunner = errors.New("llmcaller/codex: thread runner is nil")
	// ErrMissingSchemaJSON reports a request without an output schema.
	ErrMissingSchemaJSON = errors.New("llmcaller/codex: output schema JSON is required")
)

// ThreadRunner is the exact subset of codexsdk.ThreadRunner used by Caller.
type ThreadRunner interface {
	Start(context.Context, codexsdk.StartThreadRunRequest) (codexsdk.StartedThreadRun, error)
	StartStream(context.Context, codexsdk.StartThreadRunRequest) (*codexsdk.Stream[codexsdk.StartedThreadRun], error)
}

// Options configures a Caller with exact generated Codex defaults.
type Options struct {
	Runner   ThreadRunner
	Defaults codexsdk.StartThreadRunRequest

	profile safetyProfile
}

// Details retains the exact Codex run behind a neutral response.
type Details struct {
	Run codexsdk.StartedThreadRun
}

// ProviderName returns the stable provider identity used by neutral evidence.
func (Details) ProviderName() string { return "codex" }

// Caller adapts neutral structured calls to exact Codex thread runs.
type Caller struct {
	runner   ThreadRunner
	defaults codexsdk.StartThreadRunRequest
	profile  safetyProfile
}

type safetyProfile uint8

const profileReadOnlyEphemeral safetyProfile = 1

var _ llmadapter.Caller = (*Caller)(nil)

// New validates options and clones mutable defaults.
func New(options Options) (*Caller, error) {
	if isNil(options.Runner) {
		return nil, ErrNilThreadRunner
	}
	if options.Defaults.Turn.ThreadID != "" {
		return nil, errors.New("llmcaller/codex: Defaults.Turn.ThreadID is adapter-owned")
	}
	if options.Defaults.Turn.Input != nil {
		return nil, errors.New("llmcaller/codex: Defaults.Turn.Input is adapter-owned")
	}
	if options.Defaults.Turn.OutputSchema != nil {
		return nil, errors.New("llmcaller/codex: Defaults.Turn.OutputSchema is adapter-owned")
	}
	if options.profile == profileReadOnlyEphemeral {
		if err := validateReadOnlyEphemeralProfile(options.Defaults); err != nil {
			return nil, err
		}
		enforceReadOnlyEphemeralProfile(&options.Defaults)
	}
	defaults, err := cloneStartRequest(options.Defaults)
	if err != nil {
		return nil, fmt.Errorf("llmcaller/codex: clone defaults: %w", err)
	}
	return &Caller{runner: options.Runner, defaults: defaults, profile: options.profile}, nil
}

// ReadOnlyEphemeralOptions returns the named read-only, never-approve,
// ephemeral Codex profile. New rejects conflicting profile-owned defaults,
// fills unset profile fields, and reapplies the profile before each runner
// invocation.
func ReadOnlyEphemeralOptions(runner ThreadRunner) Options {
	return Options{
		Runner: runner,
		Defaults: codexsdk.StartThreadRunRequest{
			Thread: protocolv2.ThreadStartParams{
				ApprovalPolicy: protocolv2.Value(protocolv2.NewAskForApprovalNever()),
				Ephemeral:      protocolv2.Value(true),
				Sandbox:        protocolv2.Value(protocolv2.SandboxModeReadOnly),
			},
			Turn: protocolv2.TurnStartParams{
				ApprovalPolicy: protocolv2.Value(protocolv2.NewAskForApprovalNever()),
				SandboxPolicy:  protocolv2.Value(protocolv2.NewSandboxPolicyReadOnly(protocolv2.SandboxPolicyReadOnly{})),
			},
		},
		profile: profileReadOnlyEphemeral,
	}
}

// Call executes the detailed path and projects its available neutral facts.
func (c *Caller) Call(ctx context.Context, request llmadapter.Request) (llmadapter.Response, error) {
	run, runErr := c.CallDetailed(ctx, request)
	if !hasRunEvidence(run) {
		return llmadapter.Response{}, runErr
	}
	response, projectionErr := responseFromRun(run)
	return response, errors.Join(runErr, projectionErr)
}

// CallDetailed executes a structured call and returns the exact run, including
// partial evidence when an error also occurs.
func (c *Caller) CallDetailed(ctx context.Context, request llmadapter.Request) (codexsdk.StartedThreadRun, error) {
	if c == nil || isNil(c.runner) {
		return codexsdk.StartedThreadRun{}, ErrNilThreadRunner
	}
	startRequest, err := c.request(request)
	if err != nil {
		return codexsdk.StartedThreadRun{}, err
	}
	run, runErr := c.runner.Start(ctx, startRequest)
	cloned, cloneErr := cloneStartedRun(run)
	if cloneErr != nil {
		cloned = run
	}
	profileErr := c.validateProfile(cloned)
	return cloned, errors.Join(runErr, cloneErr, profileErr)
}

// CallStream starts the same exact request through the SDK streaming path.
func (c *Caller) CallStream(ctx context.Context, request llmadapter.Request) (*codexsdk.Stream[codexsdk.StartedThreadRun], error) {
	if c == nil || isNil(c.runner) {
		return nil, ErrNilThreadRunner
	}
	startRequest, err := c.request(request)
	if err != nil {
		return nil, err
	}
	return c.runner.StartStream(ctx, startRequest)
}

func (c *Caller) request(request llmadapter.Request) (codexsdk.StartThreadRunRequest, error) {
	outputSchema, err := StrictOutputSchemaFromJSON(request.OutputSchema)
	if err != nil {
		return codexsdk.StartThreadRunRequest{}, err
	}
	startRequest, err := cloneStartRequest(c.defaults)
	if err != nil {
		return codexsdk.StartThreadRunRequest{}, err
	}
	if c.profile == profileReadOnlyEphemeral {
		enforceReadOnlyEphemeralProfile(&startRequest)
	}
	startRequest.Turn.ThreadID = ""
	startRequest.Turn.Input = []protocolv2.UserInput{
		protocolv2.NewUserInputText(protocolv2.UserInputText{Text: request.Prompt}),
	}
	startRequest.Turn.OutputSchema = &outputSchema
	return startRequest, nil
}

func validateReadOnlyEphemeralProfile(defaults codexsdk.StartThreadRunRequest) error {
	if defaults.Thread.Ephemeral != nil && defaults.Thread.Ephemeral.Value != nil && !*defaults.Thread.Ephemeral.Value {
		return errors.New("llmcaller/codex: read-only profile Defaults.Thread.Ephemeral must be true")
	}
	if defaults.Thread.Sandbox != nil && defaults.Thread.Sandbox.Value != nil && *defaults.Thread.Sandbox.Value != protocolv2.SandboxModeReadOnly {
		return errors.New("llmcaller/codex: read-only profile Defaults.Thread.Sandbox must be read-only")
	}
	if defaults.Thread.ApprovalPolicy != nil && defaults.Thread.ApprovalPolicy.Value != nil && defaults.Thread.ApprovalPolicy.Value.Kind() != protocolv2.AskForApprovalKindNever {
		return errors.New("llmcaller/codex: read-only profile Defaults.Thread.ApprovalPolicy must be never")
	}
	if defaults.Turn.SandboxPolicy != nil && defaults.Turn.SandboxPolicy.Value != nil && defaults.Turn.SandboxPolicy.Value.Kind() != protocolv2.SandboxPolicyKindReadOnly {
		return errors.New("llmcaller/codex: read-only profile Defaults.Turn.SandboxPolicy must be read-only")
	}
	if defaults.Turn.ApprovalPolicy != nil && defaults.Turn.ApprovalPolicy.Value != nil && defaults.Turn.ApprovalPolicy.Value.Kind() != protocolv2.AskForApprovalKindNever {
		return errors.New("llmcaller/codex: read-only profile Defaults.Turn.ApprovalPolicy must be never")
	}
	return nil
}

func enforceReadOnlyEphemeralProfile(request *codexsdk.StartThreadRunRequest) {
	request.Thread.Ephemeral = protocolv2.Value(true)
	request.Thread.Sandbox = protocolv2.Value(protocolv2.SandboxModeReadOnly)
	request.Thread.ApprovalPolicy = protocolv2.Value(protocolv2.NewAskForApprovalNever())
	request.Turn.SandboxPolicy = protocolv2.Value(protocolv2.NewSandboxPolicyReadOnly(protocolv2.SandboxPolicyReadOnly{}))
	request.Turn.ApprovalPolicy = protocolv2.Value(protocolv2.NewAskForApprovalNever())
}

func (c *Caller) validateProfile(run codexsdk.StartedThreadRun) error {
	if c.profile != profileReadOnlyEphemeral || run.Start.Thread.ID == "" {
		return nil
	}
	if run.Start.ApprovalPolicy.Kind() != protocolv2.AskForApprovalKindNever {
		return errors.New("llmcaller/codex: read-only profile effective approval policy is not never")
	}
	if run.Start.Sandbox.Kind() != protocolv2.SandboxPolicyKindReadOnly {
		return errors.New("llmcaller/codex: read-only profile effective sandbox is not read-only")
	}
	if !run.Start.Thread.Ephemeral {
		return errors.New("llmcaller/codex: read-only profile effective thread is not ephemeral")
	}
	return nil
}

func responseFromRun(run codexsdk.StartedThreadRun) (llmadapter.Response, error) {
	cloned, cloneErr := cloneStartedRun(run)
	if cloneErr != nil {
		cloned = run
	}
	response := llmadapter.Response{
		FinalResponse: cloned.Run.FinalResponse,
		Execution: llmadapter.ExecutionEvidence{
			ProviderName:   "codex",
			EffectiveModel: effectiveModel(cloned),
			Usage:          neutralUsage(cloned.Run.Usage),
		},
		ProviderDetails: Details{Run: cloned},
	}
	return response, cloneErr
}

func hasRunEvidence(run codexsdk.StartedThreadRun) bool {
	return !reflect.DeepEqual(run, codexsdk.StartedThreadRun{})
}

func effectiveModel(run codexsdk.StartedThreadRun) string {
	model := run.Start.Model
	for _, notification := range run.Run.Notifications {
		if rerouted, ok := notification.AsModelRerouted(); ok {
			model = rerouted.Params.ToModel
		}
	}
	return model
}

func neutralUsage(usage *protocolv2.ThreadTokenUsage) *llmadapter.TokenUsage {
	if usage == nil {
		return nil
	}
	return &llmadapter.TokenUsage{
		InputTokens:           usage.Total.InputTokens,
		CachedInputTokens:     usage.Total.CachedInputTokens,
		OutputTokens:          usage.Total.OutputTokens,
		ReasoningOutputTokens: usage.Total.ReasoningOutputTokens,
	}
}

// SchemaPolicyError identifies a stable schema-policy kind and JSON pointer.
type SchemaPolicyError struct {
	Path string
	Kind string
	Err  error
}

func (e *SchemaPolicyError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("llmcaller/codex: schema policy %s at %s: %v", e.Kind, e.Path, e.Err)
}

func (e *SchemaPolicyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StrictOutputSchemaFromJSON applies the Codex structured-output schema policy
// without discarding unknown JSON keyword values.
func StrictOutputSchemaFromJSON(raw json.RawMessage) (protocolv2.OutputSchema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return protocolv2.OutputSchema{}, ErrMissingSchemaJSON
	}
	parsed, err := protocolv2.ParseJSONValue(raw)
	if err != nil {
		return protocolv2.OutputSchema{}, &SchemaPolicyError{Path: "", Kind: "invalid_json", Err: err}
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return protocolv2.OutputSchema{}, &SchemaPolicyError{Path: "", Kind: "invalid_json", Err: err}
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return protocolv2.OutputSchema{}, &SchemaPolicyError{Path: "", Kind: "invalid_json", Err: err}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if usesLegacyTupleItems(root) {
		compiler.DefaultDraft(jsonschema.Draft7)
	}
	compiler.UseLoader(rejectSchemaResourceLoader{})
	const schemaResourceURL = "https://llmcaller.invalid/output-schema.json"
	if err := compiler.AddResource(schemaResourceURL, root); err != nil {
		return protocolv2.OutputSchema{}, &SchemaPolicyError{Path: "", Kind: "invalid_schema", Err: err}
	}
	transformer := schemaTransformer{root: root, compiler: compiler, resourceURL: schemaResourceURL}
	if err := transformer.walk(root, "", nil); err != nil {
		return protocolv2.OutputSchema{}, err
	}
	out, err := json.Marshal(root)
	if err != nil {
		return protocolv2.OutputSchema{}, &SchemaPolicyError{Path: "", Kind: "marshal", Err: err}
	}
	schema, err := protocolv2.OutputSchemaFromJSON(out)
	if err != nil {
		return protocolv2.OutputSchema{}, &SchemaPolicyError{Path: "", Kind: "invalid_schema", Err: err}
	}
	return schema, nil
}

type schemaTransformer struct {
	root        any
	compiler    *jsonschema.Compiler
	resourceURL string
}

type rejectSchemaResourceLoader struct{}

func (rejectSchemaResourceLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema resource %q is unsupported", url)
}

func (t schemaTransformer) walk(value any, path string, refs map[string]bool) error {
	object, ok := value.(map[string]any)
	if !ok {
		if _, boolean := value.(bool); boolean {
			return nil
		}
		return &SchemaPolicyError{Path: path, Kind: "invalid_subschema", Err: errors.New("subschema must be an object or boolean")}
	}
	if refValue, exists := object["$ref"]; exists {
		ref, ok := refValue.(string)
		if !ok {
			return &SchemaPolicyError{Path: path + "/$ref", Kind: "invalid_ref", Err: errors.New("$ref must be a string")}
		}
		if !strings.HasPrefix(ref, "#") {
			return &SchemaPolicyError{Path: path + "/$ref", Kind: "external_ref", Err: fmt.Errorf("external reference %q is unsupported", ref)}
		}
		if refs[ref] {
			return &SchemaPolicyError{Path: path + "/$ref", Kind: "cyclic_ref", Err: fmt.Errorf("cyclic reference %q", ref)}
		}
		resolved, err := resolveLocalRef(t.root, ref)
		if err != nil {
			return &SchemaPolicyError{Path: path + "/$ref", Kind: "unresolvable_ref", Err: err}
		}
		nextRefs := copyRefSet(refs)
		nextRefs[ref] = true
		if err := t.walk(resolved, refPath(ref), nextRefs); err != nil {
			return err
		}
	}
	if _, exists := object["$dynamicRef"]; exists {
		return &SchemaPolicyError{Path: path + "/$dynamicRef", Kind: "unsupported_dynamic_ref", Err: errors.New("dynamic references are not supported by the Codex schema policy")}
	}
	if properties, exists := objectMap(object["properties"]); exists {
		required, err := requiredSet(object["required"], path)
		if err != nil {
			return err
		}
		for name, property := range properties {
			propertyPath := path + "/properties/" + escapePointer(name)
			if err := t.walk(property, propertyPath, refs); err != nil {
				return err
			}
			if !required[name] {
				admits, err := t.admitsNull(propertyPath)
				if err != nil {
					return &SchemaPolicyError{Path: propertyPath, Kind: "nullable_analysis", Err: err}
				}
				if !admits {
					return &SchemaPolicyError{Path: propertyPath, Kind: "optional_non_nullable", Err: errors.New("optional property does not admit null")}
				}
				required[name] = true
			}
		}
		if len(properties) > 0 {
			names := make([]string, 0, len(required))
			for name := range required {
				names = append(names, name)
			}
			sort.Strings(names)
			object["required"] = names
		}
	} else if _, present := object["properties"]; present {
		return &SchemaPolicyError{Path: path + "/properties", Kind: "invalid_properties", Err: errors.New("properties must be an object")}
	}
	for _, key := range []string{"additionalProperties", "unevaluatedProperties", "propertyNames", "additionalItems", "contains", "unevaluatedItems", "not", "if", "then", "else", "contentSchema"} {
		if child, exists := object[key]; exists {
			if err := t.walk(child, path+"/"+escapePointer(key), refs); err != nil {
				return err
			}
		}
	}
	if items, exists := object["items"]; exists {
		if list, ok := items.([]any); ok {
			for index, child := range list {
				if err := t.walk(child, fmt.Sprintf("%s/items/%d", path, index), refs); err != nil {
					return err
				}
			}
		} else if err := t.walk(items, path+"/items", refs); err != nil {
			return err
		}
	}
	for _, key := range []string{"properties", "patternProperties", "dependentSchemas", "$defs", "definitions"} {
		children, exists := objectMap(object[key])
		if !exists {
			continue
		}
		for name, child := range children {
			if key == "properties" {
				continue
			}
			if err := t.walk(child, path+"/"+escapePointer(key)+"/"+escapePointer(name), refs); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if children, exists := object[key]; exists {
			list, ok := children.([]any)
			if !ok {
				return &SchemaPolicyError{Path: path + "/" + key, Kind: "invalid_subschemas", Err: errors.New("keyword must be an array")}
			}
			for index, child := range list {
				if err := t.walk(child, fmt.Sprintf("%s/%s/%d", path, key, index), refs); err != nil {
					return err
				}
			}
		}
	}
	if dependencies, exists := objectMap(object["dependencies"]); exists {
		for name, dependency := range dependencies {
			if _, propertyList := dependency.([]any); propertyList {
				continue
			}
			if err := t.walk(dependency, path+"/dependencies/"+escapePointer(name), refs); err != nil {
				return err
			}
		}
	} else if _, present := object["dependencies"]; present {
		return &SchemaPolicyError{Path: path + "/dependencies", Kind: "invalid_dependencies", Err: errors.New("dependencies must be an object")}
	}
	return nil
}

func (t schemaTransformer) admitsNull(path string) (bool, error) {
	schema, err := t.compiler.Compile(t.resourceURL + "#" + path)
	if err != nil {
		return false, err
	}
	return schema.Validate(nil) == nil, nil
}

func requiredSet(value any, path string) (map[string]bool, error) {
	set := map[string]bool{}
	if value == nil {
		return set, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &SchemaPolicyError{Path: path + "/required", Kind: "invalid_required", Err: errors.New("required must be an array")}
	}
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, &SchemaPolicyError{Path: path + "/required", Kind: "invalid_required", Err: errors.New("required entries must be strings")}
		}
		set[name] = true
	}
	return set, nil
}

func resolveLocalRef(root any, ref string) (any, error) {
	if ref == "#" {
		return root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported local reference %q", ref)
	}
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference %q traverses a non-object", ref)
		}
		current, ok = object[token]
		if !ok {
			return nil, fmt.Errorf("reference %q does not exist", ref)
		}
	}
	return current, nil
}

func objectMap(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func usesLegacyTupleItems(value any) bool {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			if usesLegacyTupleItems(child) {
				return true
			}
		}
	case map[string]any:
		if _, legacy := value["items"].([]any); legacy {
			return true
		}
		for _, child := range value {
			if usesLegacyTupleItems(child) {
				return true
			}
		}
	}
	return false
}

func copyRefSet(refs map[string]bool) map[string]bool {
	copied := make(map[string]bool, len(refs)+1)
	for ref, present := range refs {
		copied[ref] = present
	}
	return copied
}

func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func refPath(ref string) string {
	if ref == "#" {
		return ""
	}
	return strings.TrimPrefix(ref, "#")
}

func cloneStartRequest(request codexsdk.StartThreadRunRequest) (codexsdk.StartThreadRunRequest, error) {
	var cloned codexsdk.StartThreadRunRequest
	if err := cloneGenerated(request.Thread, &cloned.Thread); err != nil {
		return cloned, err
	}
	turn := request.Turn
	nilInput := turn.Input == nil
	if nilInput {
		turn.Input = []protocolv2.UserInput{}
	}
	if err := cloneGenerated(turn, &cloned.Turn); err != nil {
		return cloned, err
	}
	if nilInput {
		cloned.Turn.Input = nil
	}
	return cloned, nil
}

func cloneStartedRun(run codexsdk.StartedThreadRun) (codexsdk.StartedThreadRun, error) {
	var cloned codexsdk.StartedThreadRun
	if !reflect.DeepEqual(run.Start, protocolv2.ThreadStartResponse{}) {
		if err := cloneGenerated(run.Start, &cloned.Start); err != nil {
			return cloned, err
		}
	}
	cloned.Run = run.Run
	if run.Run.Turn.ID != "" {
		if err := cloneGenerated(run.Run.Turn, &cloned.Run.Turn); err != nil {
			return cloned, err
		}
	}
	if run.Run.Usage != nil {
		var usage protocolv2.ThreadTokenUsage
		if err := cloneGenerated(*run.Run.Usage, &usage); err != nil {
			return cloned, err
		}
		cloned.Run.Usage = &usage
	}
	cloned.Run.Notifications = make([]protocolv2.ServerNotification, len(run.Run.Notifications))
	for index := range run.Run.Notifications {
		if err := cloneGenerated(run.Run.Notifications[index], &cloned.Run.Notifications[index]); err != nil {
			return cloned, err
		}
	}
	cloned.Run.Diagnostics = append([]codexsdk.DiagnosticRef(nil), run.Run.Diagnostics...)
	return cloned, nil
}

func cloneGenerated(source any, destination any) error {
	raw, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, destination)
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
