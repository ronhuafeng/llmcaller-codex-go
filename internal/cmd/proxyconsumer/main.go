// Command proxyconsumer verifies a tagged llmcaller release exactly as an
// external Go module consumer resolves it through proxy.golang.org.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	callerModule   = "github.com/ronhuafeng/llmcaller-codex-go"
	llmkitModule   = "github.com/ronhuafeng/llmkit-go"
	codexsdkModule = "github.com/ronhuafeng/codexsdk-go"
)

var (
	callerVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	stableVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
)

type moduleVersion struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Sum      string `json:"Sum"`
	GoModSum string `json:"GoModSum"`
}

type moduleEdit struct {
	Replace []any `json:"Replace"`
	Exclude []any `json:"Exclude"`
}

type config struct {
	tag                string
	proxy              string
	propagationTimeout time.Duration
	retryInterval      time.Duration
	validationTimeout  time.Duration
	commandTimeout     time.Duration
}

func main() {
	var options config
	flag.StringVar(&options.tag, "tag", os.Getenv("GITHUB_REF_NAME"), "exact caller tag to resolve")
	flag.StringVar(&options.proxy, "proxy", "https://proxy.golang.org", "exclusive Go module proxy")
	flag.DurationVar(&options.propagationTimeout, "timeout", 10*time.Minute, "maximum proxy propagation wait")
	flag.DurationVar(&options.retryInterval, "retry-interval", 15*time.Second, "proxy retry interval")
	flag.DurationVar(&options.validationTimeout, "validation-timeout", 10*time.Minute, "maximum post-resolution validation time")
	flag.DurationVar(&options.commandTimeout, "command-timeout", 5*time.Minute, "maximum time for one Go command")
	flag.Parse()
	if err := run(context.Background(), options); err != nil {
		fmt.Fprintln(os.Stderr, "proxy consumer gate:", err)
		os.Exit(1)
	}
}

func run(parent context.Context, options config) error {
	if !callerVersionRE.MatchString(options.tag) {
		return fmt.Errorf("tag %q is not an exact Go module release version", options.tag)
	}
	if options.proxy == "" || options.propagationTimeout <= 0 || options.retryInterval <= 0 || options.validationTimeout <= 0 || options.commandTimeout <= 0 {
		return errors.New("proxy and all timeout/retry bounds must be set")
	}
	workdir, err := os.MkdirTemp("", "llmcaller-proxy-consumer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module example.test/proxy-consumer\n\ngo 1.23.0\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte(consumerSource), 0o600); err != nil {
		return err
	}
	environment := proxyEnvironment(options.proxy, workdir)
	propagationCtx, cancelPropagation := context.WithTimeout(parent, options.propagationTimeout)
	var resolved moduleVersion
	err = retryUntilAvailable(propagationCtx, options.retryInterval, func() error {
		output, commandErr := commandOutputBounded(propagationCtx, options.commandTimeout, workdir, environment, "go", "list", "-m", "-json", callerModule+"@"+options.tag)
		if commandErr != nil {
			return commandErr
		}
		if decodeErr := json.Unmarshal(output, &resolved); decodeErr != nil {
			return fmt.Errorf("decode proxy resolution: %w", decodeErr)
		}
		if resolved.Path != callerModule || resolved.Version != options.tag {
			return fmt.Errorf("resolved caller = %s@%s, want %s@%s", resolved.Path, resolved.Version, callerModule, options.tag)
		}
		return nil
	})
	cancelPropagation()
	if err != nil {
		return fmt.Errorf("resolve %s@%s through %s: %w", callerModule, options.tag, options.proxy, err)
	}
	validationCtx, cancelValidation := context.WithTimeout(parent, options.validationTimeout)
	defer cancelValidation()
	for _, invocation := range [][]string{
		{"get", callerModule + "@" + options.tag},
		{"mod", "tidy"},
		{"mod", "verify"},
	} {
		if _, err := commandOutputBounded(validationCtx, options.commandTimeout, workdir, environment, "go", invocation...); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(workdir, "go.work")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clean consumer contains go.work: %v", err)
	}
	goWork, err := commandOutputBounded(validationCtx, options.commandTimeout, workdir, environment, "go", "env", "GOWORK")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(goWork)) != "off" {
		return fmt.Errorf("GOWORK = %q, want off", strings.TrimSpace(string(goWork)))
	}
	editJSON, err := commandOutputBounded(validationCtx, options.commandTimeout, workdir, environment, "go", "mod", "edit", "-json")
	if err != nil {
		return err
	}
	var edit moduleEdit
	if err := json.Unmarshal(editJSON, &edit); err != nil {
		return fmt.Errorf("decode go.mod edit: %w", err)
	}
	if err := validateModuleEdit(edit); err != nil {
		return err
	}
	graphJSON, err := commandOutputBounded(validationCtx, options.commandTimeout, workdir, environment, "go", "list", "-m", "-json", "all")
	if err != nil {
		return err
	}
	modules, err := decodeModuleGraph(graphJSON)
	if err != nil {
		return err
	}
	if err := validateResolvedModules(modules, options.tag); err != nil {
		return err
	}
	evidence, err := commandOutputBounded(validationCtx, options.commandTimeout, workdir, environment, "go", "run", ".")
	if err != nil {
		return err
	}
	fmt.Printf("proxy=%s caller_tag=%s\n", options.proxy, options.tag)
	for _, module := range selectedModules(modules) {
		fmt.Printf("module=%s version=%s sum=%s gomodsum=%s\n", module.Path, module.Version, module.Sum, module.GoModSum)
	}
	fmt.Printf("call_evidence=%s\n", strings.TrimSpace(string(evidence)))
	return nil
}

func retryUntilAvailable(ctx context.Context, interval time.Duration, attempt func() error) error {
	var lastErr error
	for {
		if err := attempt(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: last proxy error: %v", ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func validateModuleEdit(edit moduleEdit) error {
	if len(edit.Replace) != 0 {
		return errors.New("clean consumer go.mod contains replace directives")
	}
	if len(edit.Exclude) != 0 {
		return errors.New("clean consumer go.mod contains exclude directives")
	}
	return nil
}

func decodeModuleGraph(data []byte) ([]moduleVersion, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var modules []moduleVersion
	for {
		var module moduleVersion
		if err := decoder.Decode(&module); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode module graph: %w", err)
		}
		modules = append(modules, module)
	}
	return modules, nil
}

func validateResolvedModules(modules []moduleVersion, callerTag string) error {
	byPath := make(map[string]moduleVersion, len(modules))
	for _, module := range modules {
		byPath[module.Path] = module
	}
	caller, ok := byPath[callerModule]
	if !ok || caller.Version != callerTag {
		return fmt.Errorf("resolved caller = %s@%s, want %s@%s", caller.Path, caller.Version, callerModule, callerTag)
	}
	for _, path := range []string{callerModule, llmkitModule, codexsdkModule} {
		module, ok := byPath[path]
		if !ok {
			return fmt.Errorf("resolved module graph is missing %s", path)
		}
		if path != callerModule && !stableVersionRE.MatchString(module.Version) {
			return fmt.Errorf("%s resolved to non-stable version %q", path, module.Version)
		}
		if module.Sum == "" || module.GoModSum == "" {
			return fmt.Errorf("%s is missing module sum evidence", path)
		}
	}
	return nil
}

func selectedModules(modules []moduleVersion) []moduleVersion {
	wanted := map[string]bool{callerModule: true, llmkitModule: true, codexsdkModule: true}
	selected := make([]moduleVersion, 0, len(wanted))
	for _, module := range modules {
		if wanted[module.Path] {
			selected = append(selected, module)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
	return selected
}

func proxyEnvironment(proxy string, workdir string) []string {
	blocked := map[string]bool{
		"GO111MODULE": true, "GOCACHE": true, "GOENV": true, "GOFLAGS": true, "GOMODCACHE": true,
		"GOPATH": true, "GOTOOLCHAIN": true, "GOVCS": true,
		"GONOPROXY": true, "GONOSUMDB": true, "GOPRIVATE": true,
		"GOPROXY": true, "GOSUMDB": true, "GOWORK": true,
	}
	environment := make([]string, 0, len(os.Environ())+6)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			environment = append(environment, item)
		}
	}
	return append(environment,
		"GO111MODULE=on", "GOCACHE="+filepath.Join(workdir, "buildcache"), "GOENV=off", "GOFLAGS=",
		"GOMODCACHE="+filepath.Join(workdir, "modcache"), "GOPATH="+filepath.Join(workdir, "gopath"),
		"GOTOOLCHAIN=local", "GOVCS=*:off",
		"GONOPROXY=", "GONOSUMDB=", "GOPRIVATE=",
		"GOPROXY="+proxy, "GOSUMDB=sum.golang.org", "GOWORK=off",
	)
}

func commandOutputBounded(parent context.Context, timeout time.Duration, dir string, environment []string, name string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return commandOutput(ctx, dir, environment, name, arguments...)
}

func commandOutput(ctx context.Context, dir string, environment []string, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = dir
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

const consumerSource = `package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/ronhuafeng/codexsdk-go/codexsdk"
	"github.com/ronhuafeng/codexsdk-go/codexsdk/protocolv2"
	"github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex"
	"github.com/ronhuafeng/llmkit-go/llmadapter"
)

type fakeTransport struct{}

func (fakeTransport) Start(context.Context, codexsdk.StartThreadRunRequest) (codexsdk.StartedThreadRun, error) {
	return codexsdk.StartedThreadRun{
		Start: protocolv2.ThreadStartResponse{
			ApprovalPolicy: protocolv2.NewAskForApprovalNever(),
			ApprovalsReviewer: protocolv2.ApprovalsReviewerUser,
			CWD: "/proxy-consumer",
			Model: "proxy-fake",
			ModelProvider: "fake",
			Sandbox: protocolv2.NewSandboxPolicyReadOnly(protocolv2.SandboxPolicyReadOnly{}),
			Thread: protocolv2.Thread{
				CliVersion: "proxy-consumer", CWD: "/proxy-consumer", Ephemeral: true,
				ID: "proxy-thread", ModelProvider: "fake", Preview: "typed proxy canary",
				SessionID: "proxy-session", Source: protocolv2.NewSessionSourceAppServer(),
				Status: protocolv2.NewThreadStatusIdle(), Turns: []protocolv2.Turn{},
			},
		},
		Run: codexsdk.ThreadRunResult{
			FinalResponse: "{\"answer\":true}",
			Turn: protocolv2.Turn{ID: "proxy-turn", Items: []protocolv2.ThreadItem{}, Status: protocolv2.TurnStatusCompleted},
		},
	}, nil
}

func (fakeTransport) StartStream(context.Context, codexsdk.StartThreadRunRequest) (*codexsdk.Stream[codexsdk.StartedThreadRun], error) {
	return nil, errors.New("stream is not used by the proxy consumer")
}

func main() {
	caller, err := codexcaller.New(codexcaller.ReadOnlyEphemeralOptions(fakeTransport{}))
	if err != nil {
		panic(err)
	}
	result, err := llmadapter.ValueDetailed[struct {
		Answer bool ` + "`json:\"answer\"`" + `
	}](context.Background(), caller, "return a typed answer")
	if err != nil {
		panic(err)
	}
	details, ok := result.Response.ProviderDetails.(codexcaller.Details)
	if !ok || !result.Value.Answer || details.Run.Run.FinalResponse == "" {
		panic(fmt.Sprintf("incomplete three-layer evidence: %#v", result))
	}
	evidence := struct {
		Answer bool ` + "`json:\"answer\"`" + `
		Provider string ` + "`json:\"provider\"`" + `
		Model string ` + "`json:\"model\"`" + `
		ThreadID string ` + "`json:\"thread_id\"`" + `
	}{result.Value.Answer, result.Response.Execution.ProviderName, result.Response.Execution.EffectiveModel, details.Run.Start.Thread.ID}
	if err := json.NewEncoder(os.Stdout).Encode(evidence); err != nil {
		panic(err)
	}
}
`
