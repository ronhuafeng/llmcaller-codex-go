// Command proxyconsumer verifies a tagged llmcaller release exactly as an
// external Go module consumer resolves it through proxy.golang.org.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	"github.com/ronhuafeng/llmcaller-codex-go/internal/compatibilitycontract"
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
	Path     string         `json:"Path"`
	Version  string         `json:"Version"`
	Dir      string         `json:"Dir"`
	Sum      string         `json:"Sum"`
	GoModSum string         `json:"GoModSum"`
	Replace  *moduleVersion `json:"Replace"`
	Origin   *moduleOrigin  `json:"Origin"`
}

type moduleOrigin struct {
	VCS  string `json:"VCS"`
	URL  string `json:"URL"`
	Hash string `json:"Hash"`
	Ref  string `json:"Ref"`
}

type moduleEdit struct {
	Replace []any `json:"Replace"`
	Exclude []any `json:"Exclude"`
}

type compatibilityTuple struct {
	FormatVersion  int
	CheckoutDigest string
	ProxyDigest    string
	Versions       map[string]string
}

type config struct {
	tag                     string
	compatibilityPath       string
	moduleCompatibilityPath string
	proxy                   string
	propagationTimeout      time.Duration
	retryInterval           time.Duration
	validationTimeout       time.Duration
	commandTimeout          time.Duration
}

func main() {
	var options config
	flag.StringVar(&options.tag, "tag", os.Getenv("GITHUB_REF_NAME"), "exact caller tag to resolve")
	flag.StringVar(&options.compatibilityPath, "compatibility", "compatibility.json", "compatibility contract from the tagged checkout")
	flag.StringVar(&options.moduleCompatibilityPath, "module-compatibility", "compatibility.json", "compatibility contract path relative to the proxy-resolved caller module")
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
	if _, err := moduleRelativePath(".", options.moduleCompatibilityPath); err != nil {
		return err
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
	modules, err = mergeCallerResolution(modules, resolved)
	if err != nil {
		return err
	}
	tuple, err := bindCompatibilityTuple(options.compatibilityPath, options.moduleCompatibilityPath, modules, options.tag)
	if err != nil {
		return err
	}
	if err := validateResolvedModules(modules, tuple); err != nil {
		return err
	}
	evidence, err := commandOutputBounded(validationCtx, options.commandTimeout, workdir, environment, "go", "run", ".")
	if err != nil {
		return err
	}
	return writeEvidence(os.Stdout, options.proxy, tuple, modules, evidence)
}

func decodeCompatibilityTuple(data []byte, callerTag string) (compatibilityTuple, error) {
	contract, err := compatibilitycontract.Decode(data)
	if err != nil {
		return compatibilityTuple{}, err
	}
	if contract.FormatVersion != 1 {
		return compatibilityTuple{}, fmt.Errorf("compatibility format_version = %d, want 1", contract.FormatVersion)
	}
	if contract.Caller.Module != callerModule {
		return compatibilityTuple{}, fmt.Errorf("compatibility caller module = %q, want %q", contract.Caller.Module, callerModule)
	}
	wanted := map[string]bool{llmkitModule: true, codexsdkModule: true}
	versions := map[string]string{callerModule: callerTag}
	for _, dependency := range contract.Dependencies {
		if !wanted[dependency.Module] {
			return compatibilityTuple{}, fmt.Errorf("unknown compatibility dependency %q", dependency.Module)
		}
		if _, duplicate := versions[dependency.Module]; duplicate {
			return compatibilityTuple{}, fmt.Errorf("duplicate compatibility dependency %q", dependency.Module)
		}
		if !stableVersionRE.MatchString(dependency.Version) {
			return compatibilityTuple{}, fmt.Errorf("compatibility dependency %s version %q is not exact stable SemVer", dependency.Module, dependency.Version)
		}
		versions[dependency.Module] = dependency.Version
	}
	for module := range wanted {
		if _, ok := versions[module]; !ok {
			return compatibilityTuple{}, fmt.Errorf("compatibility contract missing %s", module)
		}
	}
	digest := sha256.Sum256(data)
	return compatibilityTuple{FormatVersion: contract.FormatVersion, CheckoutDigest: fmt.Sprintf("%x", digest), Versions: versions}, nil
}

func bindCompatibilityTuple(checkoutPath string, moduleCompatibilityPath string, modules []moduleVersion, callerTag string) (compatibilityTuple, error) {
	checkoutData, err := os.ReadFile(checkoutPath)
	if err != nil {
		return compatibilityTuple{}, fmt.Errorf("read checkout compatibility contract %q: %w", checkoutPath, err)
	}
	caller, err := resolvedModule(modules, callerModule)
	if err != nil {
		return compatibilityTuple{}, err
	}
	if caller.Replace != nil {
		return compatibilityTuple{}, fmt.Errorf("%s resolved through replacement module %s", callerModule, caller.Replace.Path)
	}
	if caller.Dir == "" {
		return compatibilityTuple{}, fmt.Errorf("%s has no proxy-resolved module directory", callerModule)
	}
	moduleManifestPath, err := moduleRelativePath(caller.Dir, moduleCompatibilityPath)
	if err != nil {
		return compatibilityTuple{}, err
	}
	proxyData, err := os.ReadFile(moduleManifestPath)
	if err != nil {
		return compatibilityTuple{}, fmt.Errorf("read proxy compatibility contract %q: %w", moduleManifestPath, err)
	}
	checkoutDigest := fmt.Sprintf("%x", sha256.Sum256(checkoutData))
	proxyDigest := fmt.Sprintf("%x", sha256.Sum256(proxyData))
	if checkoutDigest != proxyDigest {
		return compatibilityTuple{}, fmt.Errorf("compatibility manifest digest mismatch: checkout %s, proxy module %s", checkoutDigest, proxyDigest)
	}
	tuple, err := decodeCompatibilityTuple(checkoutData, callerTag)
	if err != nil {
		return compatibilityTuple{}, err
	}
	tuple.CheckoutDigest = checkoutDigest
	tuple.ProxyDigest = proxyDigest
	return tuple, nil
}

func mergeCallerResolution(modules []moduleVersion, resolution moduleVersion) ([]moduleVersion, error) {
	for index := range modules {
		if modules[index].Path != callerModule {
			continue
		}
		if modules[index].Version != resolution.Version {
			return nil, fmt.Errorf("resolved caller graph version %s does not match proxy query version %s", modules[index].Version, resolution.Version)
		}
		if resolution.Origin != nil {
			modules[index].Origin = resolution.Origin
		}
		return modules, nil
	}
	return nil, fmt.Errorf("resolved module graph is missing %s", callerModule)
}

func resolvedModule(modules []moduleVersion, path string) (moduleVersion, error) {
	for _, module := range modules {
		if module.Path == path {
			return module, nil
		}
	}
	return moduleVersion{}, fmt.Errorf("resolved module graph is missing %s", path)
}

func moduleRelativePath(moduleDir string, relativePath string) (string, error) {
	clean := filepath.Clean(relativePath)
	if relativePath == "" || filepath.IsAbs(relativePath) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("module compatibility path %q is not a module-relative file", relativePath)
	}
	return filepath.Join(moduleDir, clean), nil
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

func validateResolvedModules(modules []moduleVersion, expected compatibilityTuple) error {
	byPath := make(map[string]moduleVersion, len(modules))
	for _, module := range modules {
		byPath[module.Path] = module
	}
	for _, path := range []string{callerModule, llmkitModule, codexsdkModule} {
		module, ok := byPath[path]
		if !ok {
			return fmt.Errorf("resolved module graph is missing %s", path)
		}
		wantVersion, ok := expected.Versions[path]
		if !ok {
			return fmt.Errorf("compatibility tuple is missing %s", path)
		}
		if module.Version != wantVersion {
			return fmt.Errorf("%s resolved to %s, compatibility contract requires %s", path, module.Version, wantVersion)
		}
		if module.Replace != nil {
			return fmt.Errorf("%s resolved through replacement module %s", path, module.Replace.Path)
		}
		if module.Sum == "" || module.GoModSum == "" {
			return fmt.Errorf("%s is missing module sum evidence", path)
		}
	}
	return nil
}

func writeEvidence(w io.Writer, proxy string, tuple compatibilityTuple, modules []moduleVersion, callEvidence []byte) error {
	if _, err := fmt.Fprintf(w, "proxy=%s caller_tag=%s\ncompatibility_format=%d\ncheckout_compatibility_sha256=%s\nproxy_compatibility_sha256=%s\n", proxy, tuple.Versions[callerModule], tuple.FormatVersion, tuple.CheckoutDigest, tuple.ProxyDigest); err != nil {
		return err
	}
	for _, module := range selectedModules(modules) {
		if _, err := fmt.Fprintf(w, "declared_module=%s declared_version=%s resolved_module=%s resolved_version=%s sum=%s gomodsum=%s\n", module.Path, tuple.Versions[module.Path], module.Path, module.Version, module.Sum, module.GoModSum); err != nil {
			return err
		}
		if module.Path == callerModule && module.Origin != nil {
			if _, err := fmt.Fprintf(w, "caller_origin_vcs=%s caller_origin_url=%s caller_origin_hash=%s caller_origin_ref=%s\n", module.Origin.VCS, module.Origin.URL, module.Origin.Hash, module.Origin.Ref); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "call_evidence=%s\n", strings.TrimSpace(string(callEvidence)))
	return err
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
