package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestPublicAPIInventoryFailureGuidanceUsesActiveContracts(t *testing.T) {
	guidance := publicAPIMismatchMessage("type example.com/fixture.Added struct{}\n")
	if strings.Contains(guidance, "normative plan") {
		t.Fatalf("failure guidance still treats a historical plan as normative: %q", guidance)
	}
	for _, required := range []string{
		"canonical allowlist",
		"compatibility.json",
		"changelog",
		"migration",
		"public behavior",
		"clean consumer",
	} {
		if !strings.Contains(strings.ToLower(guidance), required) {
			t.Errorf("failure guidance missing %q: %q", required, guidance)
		}
	}
}

func TestPublicAPIInventoryIgnoresPrivateStructLayout(t *testing.T) {
	first := inventoryForSource(t, `package fixture
type Caller struct { private string; Exported int }
func (*Caller) Call() {}
`)
	second := inventoryForSource(t, `package fixture
type Caller struct { renamed bool; added []byte; Exported int }
func (*Caller) Call() {}
`)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("private layout changed inventory:\nfirst:  %q\nsecond: %q", first, second)
	}
	for _, declaration := range []string{
		"type example.com/fixture.Caller struct{Exported int}",
		"method example.com/fixture.Caller.Callfunc()",
	} {
		if !slices.Contains(first, declaration) {
			t.Errorf("inventory missing fully qualified declaration %q: %q", declaration, first)
		}
	}
}

func TestPublicAPIInventoryTracksExportedStructFieldsAndMethods(t *testing.T) {
	baseline := inventoryForSource(t, `package fixture
type Caller struct { private string; Exported int }
func (*Caller) Call() {}
`)
	tests := map[string]string{
		"change field": `package fixture
type Caller struct { private string; Exported string }
func (*Caller) Call() {}
`,
		"add field": `package fixture
type Caller struct { private string; Exported int; Added bool }
func (*Caller) Call() {}
`,
		"remove field": `package fixture
type Caller struct { private string }
func (*Caller) Call() {}
`,
		"change method": `package fixture
type Caller struct { private string; Exported int }
func (*Caller) Call(string) {}
`,
		"add method": `package fixture
type Caller struct { private string; Exported int }
func (*Caller) Call() {}
func (*Caller) Added() {}
`,
		"remove method": `package fixture
type Caller struct { private string; Exported int }
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			if current := inventoryForSource(t, source); reflect.DeepEqual(baseline, current) {
				t.Fatalf("exported %s did not change inventory: %q", name, current)
			}
		})
	}
}

func TestPublicAPIInventoryTracksGenericStructParameters(t *testing.T) {
	baseline := inventoryForSource(t, `package fixture
type Result[T any] struct { Value T; private T }
`)
	want := "type example.com/fixture.Result[T any] struct{Value T}"
	if !slices.Contains(baseline, want) {
		t.Fatalf("generic inventory missing %q: %q", want, baseline)
	}
	changed := inventoryForSource(t, `package fixture
type Result[T comparable] struct { Value T; private T }
`)
	if reflect.DeepEqual(baseline, changed) {
		t.Fatalf("exported type constraint change did not change inventory: %q", changed)
	}
}

func inventoryForSource(t *testing.T, source string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("example.com/fixture", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	declarations := exportedDeclarations(pkg)
	sort.Strings(declarations)
	return declarations
}
