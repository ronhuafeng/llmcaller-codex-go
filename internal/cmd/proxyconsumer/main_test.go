package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadCompatibilityTupleRejectsMalformedOrIncompleteContracts(t *testing.T) {
	valid := `{
  "format_version": 1,
  "caller": {"module": "github.com/ronhuafeng/llmcaller-codex-go", "api_inventory": "inventory.txt"},
  "dependencies": [
    {"module": "github.com/ronhuafeng/llmkit-go", "version": "v0.4.0"},
    {"module": "github.com/ronhuafeng/codexsdk-go", "version": "v0.4.0"}
  ],
  "gates": {}
}`
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "unsupported format", manifest: strings.Replace(valid, `"format_version": 1`, `"format_version": 2`, 1), want: "format_version"},
		{name: "wrong caller", manifest: strings.Replace(valid, callerModule, "example.com/wrong-caller", 1), want: "caller module"},
		{name: "missing llmkit", manifest: strings.Replace(valid, fmt.Sprintf("    {\"module\": %q, \"version\": \"v0.4.0\"},\n", llmkitModule), "", 1), want: llmkitModule},
		{name: "duplicate codexsdk", manifest: strings.Replace(valid, fmt.Sprintf(`    {"module": %q, "version": "v0.4.0"}`, llmkitModule), fmt.Sprintf(`    {"module": %q, "version": "v0.4.0"}`, codexsdkModule), 1), want: "duplicate"},
		{name: "unknown dependency", manifest: strings.Replace(valid, llmkitModule, "example.com/unknown", 1), want: "unknown compatibility dependency"},
		{name: "empty version", manifest: strings.Replace(valid, `"version": "v0.4.0"`, `"version": ""`, 1), want: "exact stable SemVer"},
		{name: "prerelease", manifest: strings.Replace(valid, `"version": "v0.4.0"`, `"version": "v0.4.1-rc.1"`, 1), want: "exact stable SemVer"},
		{name: "pseudo-version", manifest: strings.Replace(valid, `"version": "v0.4.0"`, `"version": "v0.0.0-20260713000000-deadbeefdead"`, 1), want: "exact stable SemVer"},
		{name: "trailing JSON", manifest: valid + `{}`, want: "decode compatibility contract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compatibility.json")
			if err := os.WriteFile(path, []byte(tt.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadCompatibilityTuple(path, "v0.4.0")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadCompatibilityTuple error = %v, want %q", err, tt.want)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "compatibility.json")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	tuple, err := loadCompatibilityTuple(path, "v0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if tuple.FormatVersion != 1 || len(tuple.Digest) != 64 {
		t.Fatalf("tuple metadata = %#v", tuple)
	}
	for module, want := range map[string]string{callerModule: "v0.4.0", llmkitModule: "v0.4.0", codexsdkModule: "v0.4.0"} {
		if got := tuple.Versions[module]; got != want {
			t.Errorf("tuple[%s] = %q, want %q", module, got, want)
		}
	}
}

func TestValidateResolvedModulesRequiresExactCompatibilityTuple(t *testing.T) {
	expected := compatibilityTuple{Versions: map[string]string{
		callerModule: "v0.4.0", llmkitModule: "v0.4.0", codexsdkModule: "v0.4.0",
	}}
	modules := []moduleVersion{
		{Path: callerModule, Version: "v0.4.0", Sum: "h1:caller", GoModSum: "h1:caller-mod"},
		{Path: llmkitModule, Version: "v0.4.0", Sum: "h1:kit", GoModSum: "h1:kit-mod"},
		{Path: codexsdkModule, Version: "v0.4.0", Sum: "h1:sdk", GoModSum: "h1:sdk-mod"},
	}
	if err := validateResolvedModules(modules, expected); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		index   int
		version string
		clear   string
		want    string
	}{
		{name: "caller mismatch", index: 0, version: "v0.4.1", want: "compatibility contract requires v0.4.0"},
		{name: "llmkit stable mismatch", index: 1, version: "v0.4.1", want: "compatibility contract requires v0.4.0"},
		{name: "codexsdk stable mismatch", index: 2, version: "v0.5.0", want: "compatibility contract requires v0.4.0"},
		{name: "llmkit pseudo-version", index: 1, version: "v0.0.0-20260713000000-deadbeefdead", want: "compatibility contract requires v0.4.0"},
		{name: "codexsdk prerelease", index: 2, version: "v0.5.0-rc.1", want: "compatibility contract requires v0.4.0"},
		{name: "missing sum", index: 2, clear: "sum", want: "module sum"},
		{name: "missing go.mod sum", index: 1, clear: "gomodsum", want: "module sum"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := append([]moduleVersion(nil), modules...)
			if tt.version != "" {
				bad[tt.index].Version = tt.version
			}
			switch tt.clear {
			case "sum":
				bad[tt.index].Sum = ""
			case "gomodsum":
				bad[tt.index].GoModSum = ""
			}
			if err := validateResolvedModules(bad, expected); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateResolvedModules error = %v, want %q", err, tt.want)
			}
		})
	}
	for _, missing := range []string{callerModule, llmkitModule, codexsdkModule} {
		t.Run("missing "+missing, func(t *testing.T) {
			var incomplete []moduleVersion
			for _, module := range modules {
				if module.Path != missing {
					incomplete = append(incomplete, module)
				}
			}
			if err := validateResolvedModules(incomplete, expected); err == nil || !strings.Contains(err.Error(), "missing "+missing) {
				t.Fatalf("missing graph error = %v", err)
			}
		})
	}
}

func TestWriteEvidenceRecordsDeclaredAndResolvedTuple(t *testing.T) {
	tuple := compatibilityTuple{
		FormatVersion: 1,
		Digest:        strings.Repeat("a", 64),
		Versions: map[string]string{
			callerModule: "v0.4.0", llmkitModule: "v0.4.0", codexsdkModule: "v0.4.0",
		},
	}
	modules := []moduleVersion{
		{Path: codexsdkModule, Version: "v0.4.0", Sum: "h1:sdk", GoModSum: "h1:sdk-mod"},
		{Path: callerModule, Version: "v0.4.0", Sum: "h1:caller", GoModSum: "h1:caller-mod"},
		{Path: llmkitModule, Version: "v0.4.0", Sum: "h1:kit", GoModSum: "h1:kit-mod"},
	}
	var output bytes.Buffer
	if err := writeEvidence(&output, "https://proxy.golang.org", tuple, modules, []byte(`{"answer":true}`)); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"compatibility_format=1", "compatibility_sha256=" + strings.Repeat("a", 64),
		"declared_module=" + callerModule + " declared_version=v0.4.0 resolved_module=" + callerModule + " resolved_version=v0.4.0 sum=h1:caller gomodsum=h1:caller-mod",
		"declared_module=" + llmkitModule + " declared_version=v0.4.0 resolved_module=" + llmkitModule + " resolved_version=v0.4.0 sum=h1:kit gomodsum=h1:kit-mod",
		"declared_module=" + codexsdkModule + " declared_version=v0.4.0 resolved_module=" + codexsdkModule + " resolved_version=v0.4.0 sum=h1:sdk gomodsum=h1:sdk-mod",
		`call_evidence={"answer":true}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("evidence missing %q:\n%s", want, text)
		}
	}
}

func TestValidateModuleEditRejectsOverrides(t *testing.T) {
	if err := validateModuleEdit(moduleEdit{}); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []moduleEdit{
		{Replace: []any{map[string]any{"Old": callerModule}}},
		{Exclude: []any{map[string]any{"Path": llmkitModule}}},
	} {
		if err := validateModuleEdit(edit); err == nil {
			t.Fatalf("override accepted: %#v", edit)
		}
	}
}

func TestRetryUntilAvailableIsBounded(t *testing.T) {
	attempts := 0
	err := retryUntilAvailable(context.Background(), time.Millisecond, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("not on proxy yet")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	err = retryUntilAvailable(ctx, time.Millisecond, func() error {
		cancel()
		return errors.New("still missing")
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "still missing") {
		t.Fatalf("bounded retry error = %v", err)
	}
}

func TestProxyEnvironmentUsesExclusiveCleanModuleCache(t *testing.T) {
	t.Setenv("GOPROXY", "https://untrusted.invalid,direct")
	t.Setenv("GOMODCACHE", "/shared/cache")
	t.Setenv("GOFLAGS", "-modfile=/tmp/other.mod")
	environment := proxyEnvironment("https://proxy.golang.org", "/isolated")
	values := make(map[string]string)
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	for key, want := range map[string]string{
		"GO111MODULE": "on", "GOCACHE": "/isolated/buildcache", "GOENV": "off", "GOFLAGS": "",
		"GOMODCACHE": "/isolated/modcache", "GOPATH": "/isolated/gopath",
		"GOTOOLCHAIN": "local", "GOVCS": "*:off",
		"GOPROXY": "https://proxy.golang.org", "GOPRIVATE": "", "GONOPROXY": "",
		"GOSUMDB": "sum.golang.org", "GONOSUMDB": "", "GOWORK": "off",
	} {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
