package architecture

import (
	"bytes"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMirroredUpstreamContracts(t *testing.T) {
	root := repoRoot(t)
	callerPlan, err := os.ReadFile(filepath.Join(root, "docs", "v0.2-refactor-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	modules := []struct {
		name string
		dir  string
	}{
		{"llmkit-caller", moduleDir(t, "github.com/ronhuafeng/llmkit-go")},
		{"codexsdk-caller", moduleDir(t, "github.com/ronhuafeng/codexsdk-go")},
	}
	for _, module := range modules {
		upstreamPlan, err := os.ReadFile(filepath.Join(module.dir, "docs", "v0.2-refactor-plan.md"))
		if err != nil {
			t.Fatal(err)
		}
		got := contractBlock(t, callerPlan, module.name)
		want := contractBlock(t, upstreamPlan, module.name)
		if !bytes.Equal(got, want) {
			t.Fatalf("contract %s differs from the resolved upstream module", module.name)
		}
	}
}

func moduleDir(t *testing.T, module string) string {
	t.Helper()
	command := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve %s: %v", module, err)
	}
	return strings.TrimSpace(string(output))
}

func contractBlock(t *testing.T, document []byte, name string) []byte {
	t.Helper()
	startMarker := []byte("<!-- contract:" + name + ":start -->\n")
	endMarker := []byte("\n<!-- contract:" + name + ":end -->")
	start := bytes.Index(document, startMarker)
	if start < 0 {
		t.Fatalf("missing start marker for %s", name)
	}
	start += len(startMarker)
	end := bytes.Index(document[start:], endMarker)
	if end < 0 {
		t.Fatalf("missing end marker for %s", name)
	}
	return document[start : start+end]
}

var allowedExternalImportPrefixes = []string{
	"github.com/ronhuafeng/llmkit-go",
	"github.com/ronhuafeng/codexsdk-go",
}

var forbiddenImportPrefixes = []string{
	"smart-contract",
	"github.com/ronhuafeng/llmkit-go/llmschema",
	"github.com/ronhuafeng/llmkit-go/settle",
}

func TestCodexCallerImportBoundary(t *testing.T) {
	root := repoRoot(t)

	err := filepath.WalkDir(filepath.Join(root, "llmcaller"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry == nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if isForbiddenImport(importPath) {
				t.Fatalf("Codex caller must not own schema projection, loop semantics, or business deps: %s imports %q", relPath(root, path), importPath)
			}
			if isStdlibImport(importPath) {
				continue
			}
			if !isAllowedExternalImport(importPath) {
				t.Fatalf("unexpected external dependency in Codex caller: %s imports %q", relPath(root, path), importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestImportBoundaryClassifiesStdlibAndBusinessImports(t *testing.T) {
	for _, importPath := range []string{"context", "encoding/json", "go/parser", "net/http", "path/filepath"} {
		if !isStdlibImport(importPath) {
			t.Fatalf("import %q should be classified as stdlib", importPath)
		}
	}

	for _, importPath := range []string{
		"smart-contract",
		"smart-contract/internal/companyfacts",
		"github.com/ronhuafeng/llmkit-go/llmschema",
		"github.com/ronhuafeng/llmkit-go/llmschema/internal",
		"github.com/ronhuafeng/llmkit-go/settle",
		"github.com/ronhuafeng/llmkit-go/settle/runtime",
	} {
		if !isForbiddenImport(importPath) {
			t.Fatalf("import %q should be forbidden", importPath)
		}
		if isStdlibImport(importPath) {
			t.Fatalf("import %q should not be classified as stdlib", importPath)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func relPath(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func isStdlibImport(importPath string) bool {
	pkg, err := build.Default.Import(importPath, "", build.FindOnly)
	return err == nil && pkg.Goroot
}

func isForbiddenImport(importPath string) bool {
	for _, forbiddenPrefix := range forbiddenImportPrefixes {
		if matchesImportPrefix(importPath, forbiddenPrefix) {
			return true
		}
	}
	return false
}

func isAllowedExternalImport(importPath string) bool {
	for _, allowedPrefix := range allowedExternalImportPrefixes {
		if matchesImportPrefix(importPath, allowedPrefix) {
			return true
		}
	}
	return false
}

func matchesImportPrefix(importPath string, prefix string) bool {
	return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
}
