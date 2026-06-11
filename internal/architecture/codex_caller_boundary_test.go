package architecture

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
