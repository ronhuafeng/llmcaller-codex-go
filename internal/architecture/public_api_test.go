package architecture

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestHandwrittenPublicAPI(t *testing.T) {
	root := repoRoot(t)
	loader := &callerSourceImporter{root: root, fset: token.NewFileSet()}
	pkg, err := loader.Import("github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex")
	if err != nil {
		t.Fatal(err)
	}
	declarations := exportedDeclarations(pkg)
	sort.Strings(declarations)
	actual := strings.Join(declarations, "\n") + "\n"
	path := filepath.Join(root, "internal", "architecture", "testdata", "handwritten-api.txt")
	if os.Getenv("UPDATE_HANDWRITTEN_API") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual != string(want) {
		t.Fatalf("handwritten public API changed; update the normative plan first, then review the canonical allowlist:\n%s", actual)
	}
}

type callerSourceImporter struct {
	root     string
	fset     *token.FileSet
	compiled types.Importer
}

func (i *callerSourceImporter) Import(path string) (*types.Package, error) {
	const module = "github.com/ronhuafeng/llmcaller-codex-go/llmcaller/codex"
	if path != module {
		if i.compiled == nil {
			i.compiled = importer.ForCompiler(i.fset, "gc", i.openExport)
		}
		return i.compiled.Import(path)
	}
	dir := filepath.Join(i.root, "llmcaller", "codex")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(i.fset, filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return (&types.Config{Importer: i}).Check(module, i.fset, files, nil)
}

func (i *callerSourceImporter) openExport(path string) (io.ReadCloser, error) {
	command := exec.Command("go", "list", "-export", "-json", path)
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w", path, err)
	}
	var listed struct{ Export string }
	if err := json.Unmarshal(output, &listed); err != nil {
		return nil, err
	}
	return os.Open(listed.Export)
}

func exportedDeclarations(pkg *types.Package) []string {
	qualifier := func(other *types.Package) string { return other.Path() }
	var declarations []string
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		if !object.Exported() {
			continue
		}
		declarations = append(declarations, types.ObjectString(object, qualifier))
		typeName, ok := object.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		methods := types.NewMethodSet(types.NewPointer(named))
		for index := 0; index < methods.Len(); index++ {
			method := methods.At(index).Obj()
			if method.Exported() {
				declarations = append(declarations, fmt.Sprintf("method %s.%s.%s%s", pkg.Path(), named.Obj().Name(), method.Name(), types.TypeString(method.Type(), qualifier)))
			}
		}
	}
	return declarations
}
