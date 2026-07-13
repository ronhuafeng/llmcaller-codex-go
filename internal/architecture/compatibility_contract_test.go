package architecture

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ronhuafeng/llmcaller-codex-go/internal/compatibilitycontract"
)

func TestCompatibilityContractMatchesResolvedModuleGraph(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := compatibilitycontract.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if contract.FormatVersion != 1 {
		t.Fatalf("format_version = %d, want 1", contract.FormatVersion)
	}
	if contract.Caller.Module != "github.com/ronhuafeng/llmcaller-codex-go" {
		t.Fatalf("caller module = %q", contract.Caller.Module)
	}
	contractPath(t, root, contract.Caller.APIInventory)

	command := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Version}}", "all")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve module graph: %v", err)
	}
	resolved := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		path, version, ok := strings.Cut(line, " ")
		if ok {
			resolved[path] = version
		}
	}
	wantDependencies := map[string]bool{
		"github.com/ronhuafeng/llmkit-go":   true,
		"github.com/ronhuafeng/codexsdk-go": true,
	}
	if len(contract.Dependencies) != len(wantDependencies) {
		t.Fatalf("dependencies = %d, want %d", len(contract.Dependencies), len(wantDependencies))
	}
	seenDependencies := make(map[string]bool)
	for _, dependency := range contract.Dependencies {
		if !wantDependencies[dependency.Module] {
			t.Errorf("unexpected compatibility dependency %q", dependency.Module)
		}
		if seenDependencies[dependency.Module] {
			t.Errorf("duplicate compatibility dependency %q", dependency.Module)
		}
		seenDependencies[dependency.Module] = true
		if dependency.Version == "" {
			t.Errorf("compatibility dependency %q has no version", dependency.Module)
		}
		if got := resolved[dependency.Module]; got != dependency.Version {
			t.Errorf("resolved %s = %q, contract requires %q", dependency.Module, got, dependency.Version)
		}
	}
	for module := range wantDependencies {
		if !seenDependencies[module] {
			t.Errorf("contract missing compatibility dependency %q", module)
		}
	}
	for _, name := range []string{"clean_consumer", "exported_api", "fast_canary", "full_canary", "proxy_tag_consumer", "schema_matrix"} {
		if _, ok := contract.Gates[name]; !ok {
			t.Errorf("contract missing gate %q", name)
		}
	}
	for name, gate := range contract.Gates {
		if gate.Selector == "" {
			t.Errorf("gate %q has no selector", name)
		}
		path := contractPath(t, root, gate.Path)
		if !gateSelectorExists(t, path, gate.Kind, gate.Selector) {
			t.Errorf("gate %q selector %q is not an active %s in %s", name, gate.Selector, gate.Kind, gate.Path)
		}
	}
}

func TestGateSelectorIgnoresCommentsAndUnrelatedText(t *testing.T) {
	dir := t.TempDir()
	goPath := filepath.Join(dir, "gate_test.go")
	if err := os.WriteFile(goPath, []byte("package gate\n// func TestOnlyComment(t *testing.T) {}\nvar text = `TestOnlyComment`\nfunc TestWrongSignature() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gateSelectorExists(t, goPath, "go_test", "TestOnlyComment") {
		t.Fatal("comment-only Go selector was accepted")
	}
	if gateSelectorExists(t, goPath, "go_test", "TestWrongSignature") {
		t.Fatal("invalid Go test signature was accepted")
	}
	workflowPath := filepath.Join(dir, "workflow.yml")
	if err := os.WriteFile(workflowPath, []byte("jobs:\n  # ghost-job:\n  real-job:\n    runs-on: ubuntu-latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gateSelectorExists(t, workflowPath, "github_job", "ghost-job") {
		t.Fatal("comment-only workflow selector was accepted")
	}
}

func TestContractPathRejectsSymlinkOutsideRepository(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "gate")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := resolveContractPath(root, "gate"); err == nil {
		t.Fatal("outside-repository symlink was accepted")
	}
}

func TestActiveGatesDoNotReferenceHistoricalProposal(t *testing.T) {
	root := repoRoot(t)
	needle := "v0.2-" + "refactor-plan.md"
	paths := []string{filepath.Join(root, "RELEASE.md")}
	for _, pattern := range []string{
		filepath.Join(root, ".github", "workflows", "*.yml"),
		filepath.Join(root, "internal", "architecture", "*.go"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		if filepath.Base(path) == "compatibility_contract_test.go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), needle) {
			t.Errorf("active gate %s references historical proposal", relPath(root, path))
		}
	}
}

func TestProxyTagConsumerWorkflowIsTagTriggeredAndBounded(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "proxy-tag-consumer.yml"))
	if err != nil {
		t.Fatal(err)
	}
	contract := parseProxyWorkflowContract(string(data))
	if contract.TagPattern != `["v*"]` {
		t.Errorf("proxy workflow on.push.tags = %q", contract.TagPattern)
	}
	if contract.CallerTag != `${{ github.ref_name }}` {
		t.Errorf("proxy consumer CALLER_TAG = %q", contract.CallerTag)
	}
	for _, required := range []string{
		`go run ./internal/cmd/proxyconsumer`,
		`-compatibility compatibility.json`,
		`-proxy https://proxy.golang.org`,
		`-timeout 10m`,
		`-retry-interval 15s`,
		`-validation-timeout 10m`,
		`-command-timeout 5m`,
	} {
		if !strings.Contains(contract.Run, required) {
			t.Errorf("proxy workflow run step missing %q", required)
		}
	}
}

func TestProxyWorkflowContractIgnoresUnrelatedStrings(t *testing.T) {
	fixture := `on:
  workflow_dispatch:
jobs:
  unrelated:
    env:
      CALLER_TAG: ${{ github.ref_name }}
    steps:
      - run: echo 'tags: ["v*"] go run ./internal/cmd/proxyconsumer -timeout 10m'
  proxy-consumer:
    steps:
      - name: Not the resolver
        run: echo no
`
	contract := parseProxyWorkflowContract(fixture)
	if contract.TagPattern != "" || contract.CallerTag != "" || contract.Run != "" {
		t.Fatalf("unrelated workflow strings satisfied contract: %#v", contract)
	}
}

type proxyWorkflowContract struct {
	TagPattern string
	CallerTag  string
	Run        string
}

func parseProxyWorkflowContract(workflow string) proxyWorkflowContract {
	var contract proxyWorkflowContract
	lines := strings.Split(workflow, "\n")
	inOn := false
	inPush := false
	inProxyJob := false
	inResolverStep := false
	stepSection := ""
	collectRun := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inOn = trimmed == "on:"
			inPush = false
			inProxyJob = false
		}
		if inOn && indent == 2 {
			inPush = trimmed == "push:"
		}
		if inOn && inPush && indent == 4 && strings.HasPrefix(trimmed, "tags:") {
			contract.TagPattern = strings.TrimSpace(strings.TrimPrefix(trimmed, "tags:"))
		}
		if indent == 2 {
			inProxyJob = trimmed == "proxy-consumer:"
			inResolverStep = false
			collectRun = false
		}
		if !inProxyJob {
			continue
		}
		if indent == 6 && strings.HasPrefix(trimmed, "- ") {
			inResolverStep = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:")) == "Resolve exact tag and run clean consumer"
			stepSection = ""
			collectRun = false
			continue
		}
		if !inResolverStep {
			continue
		}
		if indent == 8 {
			collectRun = trimmed == "run: |"
			switch trimmed {
			case "env:":
				stepSection = "env"
			case "run: |":
				stepSection = "run"
			default:
				stepSection = ""
			}
			continue
		}
		if stepSection == "env" && indent == 10 && strings.HasPrefix(trimmed, "CALLER_TAG:") {
			contract.CallerTag = strings.TrimSpace(strings.TrimPrefix(trimmed, "CALLER_TAG:"))
		}
		if collectRun && indent >= 10 {
			contract.Run += trimmed + "\n"
		}
	}
	return contract
}

func contractPath(t *testing.T, root string, path string) string {
	t.Helper()
	resolved, err := resolveContractPath(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func resolveContractPath(root string, path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if path == "" || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean != filepath.FromSlash(path) {
		return "", fmt.Errorf("contract path %q is not a clean repo-relative path", path)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", fmt.Errorf("resolve contract path %q: %w", path, err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("contract path %q resolves outside repository", path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat contract path %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("contract path %q is not a regular file", path)
	}
	return resolved, nil
}

func gateSelectorExists(t *testing.T, path string, kind string, selector string) bool {
	t.Helper()
	switch kind {
	case "go_test":
		if !strings.HasSuffix(path, "_test.go") {
			return false
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse Go gate %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Name.Name == selector && isGoTestFunction(function) {
				return true
			}
		}
		return false
	case "github_job":
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open workflow gate %s: %v", path, err)
		}
		defer file.Close()
		inJobs := false
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "jobs:" {
				inJobs = true
				continue
			}
			if !inJobs || strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if line[0] != ' ' && line[0] != '\t' {
				break
			}
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.TrimSuffix(strings.TrimSpace(line), ":") == selector && strings.HasSuffix(strings.TrimSpace(line), ":") {
				return true
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan workflow gate %s: %v", path, err)
		}
		return false
	default:
		t.Fatalf("unsupported gate kind %q", kind)
		return false
	}
}

func isGoTestFunction(function *ast.FuncDecl) bool {
	if function.Recv != nil || function.Type.Results != nil || function.Type.Params == nil || len(function.Type.Params.List) != 1 {
		return false
	}
	parameter := function.Type.Params.List[0]
	if len(parameter.Names) > 1 {
		return false
	}
	pointer, ok := parameter.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "T" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "testing"
}
