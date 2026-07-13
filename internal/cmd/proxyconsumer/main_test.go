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

const validCompatibilityManifest = `{
  "format_version": 1,
  "caller": {"module": "github.com/ronhuafeng/llmcaller-codex-go", "api_inventory": "inventory.txt"},
  "dependencies": [
    {"module": "github.com/ronhuafeng/llmkit-go", "version": "v0.4.0"},
    {"module": "github.com/ronhuafeng/codexsdk-go", "version": "v0.4.0"}
  ],
  "gates": {}
}`

func TestDecodeCompatibilityTupleRejectsMalformedOrIncompleteContracts(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "unsupported format", manifest: strings.Replace(validCompatibilityManifest, `"format_version": 1`, `"format_version": 2`, 1), want: "format_version"},
		{name: "wrong caller", manifest: strings.Replace(validCompatibilityManifest, callerModule, "example.com/wrong-caller", 1), want: "caller module"},
		{name: "missing llmkit", manifest: strings.Replace(validCompatibilityManifest, fmt.Sprintf("    {\"module\": %q, \"version\": \"v0.4.0\"},\n", llmkitModule), "", 1), want: llmkitModule},
		{name: "duplicate codexsdk", manifest: strings.Replace(validCompatibilityManifest, fmt.Sprintf(`    {"module": %q, "version": "v0.4.0"}`, llmkitModule), fmt.Sprintf(`    {"module": %q, "version": "v0.4.0"}`, codexsdkModule), 1), want: "duplicate"},
		{name: "unknown dependency", manifest: strings.Replace(validCompatibilityManifest, llmkitModule, "example.com/unknown", 1), want: "unknown compatibility dependency"},
		{name: "empty version", manifest: strings.Replace(validCompatibilityManifest, `"version": "v0.4.0"`, `"version": ""`, 1), want: "exact stable SemVer"},
		{name: "prerelease", manifest: strings.Replace(validCompatibilityManifest, `"version": "v0.4.0"`, `"version": "v0.4.1-rc.1"`, 1), want: "exact stable SemVer"},
		{name: "pseudo-version", manifest: strings.Replace(validCompatibilityManifest, `"version": "v0.4.0"`, `"version": "v0.0.0-20260713000000-deadbeefdead"`, 1), want: "exact stable SemVer"},
		{name: "trailing JSON", manifest: validCompatibilityManifest + `{}`, want: "decode compatibility contract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeCompatibilityTuple([]byte(tt.manifest), "v0.4.0")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decodeCompatibilityTuple error = %v, want %q", err, tt.want)
			}
		})
	}

	tuple, err := decodeCompatibilityTuple([]byte(validCompatibilityManifest), "v0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if tuple.FormatVersion != 1 {
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
		{name: "replacement module", index: 0, clear: "replace", want: "replacement module"},
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
			case "replace":
				bad[tt.index].Replace = &moduleVersion{Path: "example.com/local"}
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

func TestBindCompatibilityTupleRequiresProxyArtifactDigestMatch(t *testing.T) {
	manifest := []byte(validCompatibilityManifest)
	checkoutDir := t.TempDir()
	moduleDir := t.TempDir()
	checkoutPath := filepath.Join(checkoutDir, "compatibility.json")
	proxyPath := filepath.Join(moduleDir, "compatibility.json")
	for _, path := range []string{checkoutPath, proxyPath} {
		if err := os.WriteFile(path, manifest, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	modules := []moduleVersion{{Path: callerModule, Version: "v0.4.0", Dir: moduleDir}}
	tuple, err := bindCompatibilityTuple(checkoutPath, "compatibility.json", modules, "v0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if tuple.CheckoutDigest == "" || tuple.ProxyDigest != tuple.CheckoutDigest {
		t.Fatalf("bound digests = checkout %q proxy %q", tuple.CheckoutDigest, tuple.ProxyDigest)
	}

	if err := os.WriteFile(proxyPath, append(manifest, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = bindCompatibilityTuple(checkoutPath, "compatibility.json", modules, "v0.4.0")
	if err == nil || !strings.Contains(err.Error(), "compatibility manifest digest mismatch") {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestBindCompatibilityTupleRejectsMissingUnreadableOrReplacedProxyArtifact(t *testing.T) {
	manifest := []byte(validCompatibilityManifest)
	checkoutPath := filepath.Join(t.TempDir(), "compatibility.json")
	if err := os.WriteFile(checkoutPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		modules []moduleVersion
		setup   func(string)
		want    string
	}{
		{name: "missing caller", want: "missing " + callerModule},
		{name: "missing module directory", modules: []moduleVersion{{Path: callerModule}}, want: "has no proxy-resolved module directory"},
		{name: "missing manifest", modules: []moduleVersion{{Path: callerModule, Dir: t.TempDir()}}, want: "read proxy compatibility contract"},
		{name: "unreadable artifact", modules: []moduleVersion{{Path: callerModule, Dir: t.TempDir()}}, setup: func(dir string) {
			if err := os.Mkdir(filepath.Join(dir, "compatibility.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, want: "read proxy compatibility contract"},
		{name: "replacement", modules: []moduleVersion{{Path: callerModule, Dir: t.TempDir(), Replace: &moduleVersion{Path: "example.com/local"}}}, want: "replacement module"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(tt.modules[0].Dir)
			}
			_, err := bindCompatibilityTuple(checkoutPath, "compatibility.json", tt.modules, "v0.4.0")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("bindCompatibilityTuple error = %v, want %q", err, tt.want)
			}
		})
	}

	_, err := bindCompatibilityTuple(filepath.Join(t.TempDir(), "missing.json"), "compatibility.json", []moduleVersion{{Path: callerModule, Dir: t.TempDir()}}, "v0.4.0")
	if err == nil || !strings.Contains(err.Error(), "read checkout compatibility contract") {
		t.Fatalf("missing checkout contract error = %v", err)
	}
}

func TestModuleCompatibilityPathMustStayInsideResolvedArtifact(t *testing.T) {
	for _, path := range []string{"", ".", "../compatibility.json", "/tmp/compatibility.json"} {
		t.Run(path, func(t *testing.T) {
			if _, err := moduleRelativePath("/proxy/module", path); err == nil || !strings.Contains(err.Error(), "module-relative file") {
				t.Fatalf("moduleRelativePath(%q) error = %v", path, err)
			}
		})
	}
	got, err := moduleRelativePath("/proxy/module", "contracts/compatibility.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/proxy/module", "contracts", "compatibility.json") {
		t.Fatalf("module path = %q", got)
	}
}

func TestBindCompatibilityTupleChecksDigestBeforeContractFormat(t *testing.T) {
	checkoutPath := filepath.Join(t.TempDir(), "compatibility.json")
	moduleDir := t.TempDir()
	if err := os.WriteFile(checkoutPath, []byte(`{"format_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "compatibility.json"), []byte(`{"format_version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	modules := []moduleVersion{{Path: callerModule, Dir: moduleDir}}
	_, err := bindCompatibilityTuple(checkoutPath, "compatibility.json", modules, "v0.4.0")
	if err == nil || !strings.Contains(err.Error(), "compatibility manifest digest mismatch") {
		t.Fatalf("different unknown formats error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "compatibility.json"), []byte(`{"format_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = bindCompatibilityTuple(checkoutPath, "compatibility.json", modules, "v0.4.0")
	if err == nil || !strings.Contains(err.Error(), "format_version") {
		t.Fatalf("matching unknown format error = %v", err)
	}
}

func TestMergeCallerResolutionCarriesProxyOriginIntoGraphEvidence(t *testing.T) {
	modules := []moduleVersion{
		{Path: callerModule, Version: "v0.4.0", Dir: "/proxy/module", Sum: "h1:caller", GoModSum: "h1:caller-mod"},
		{Path: llmkitModule, Version: "v0.4.0"},
	}
	resolution := moduleVersion{
		Path: callerModule, Version: "v0.4.0",
		Origin: &moduleOrigin{VCS: "git", Hash: "deadbeef", Ref: "refs/tags/v0.4.0"},
	}
	merged, err := mergeCallerResolution(modules, resolution)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := resolvedModule(merged, callerModule)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Dir != "/proxy/module" || caller.Sum != "h1:caller" || caller.Origin == nil || caller.Origin.Hash != "deadbeef" {
		t.Fatalf("merged caller evidence = %#v", caller)
	}

	bad := append([]moduleVersion(nil), modules...)
	bad[0].Version = "v0.4.1"
	if _, err := mergeCallerResolution(bad, resolution); err == nil || !strings.Contains(err.Error(), "does not match proxy query version") {
		t.Fatalf("version mismatch error = %v", err)
	}
}

func TestResolveCallerProvenanceRequiresOriginOrTagCommit(t *testing.T) {
	originHash := strings.Repeat("a", 40)
	caller := moduleVersion{Path: callerModule, Origin: &moduleOrigin{VCS: "git", Hash: originHash, Ref: "refs/tags/v0.4.0"}}
	provenance, err := resolveCallerProvenance([]moduleVersion{caller}, "")
	if err != nil {
		t.Fatal(err)
	}
	if provenance.OriginHash != originHash || provenance.OriginRef != "refs/tags/v0.4.0" {
		t.Fatalf("origin provenance = %#v", provenance)
	}

	tagCommit := strings.Repeat("b", 40)
	provenance, err = resolveCallerProvenance([]moduleVersion{{Path: callerModule}}, tagCommit)
	if err != nil {
		t.Fatal(err)
	}
	if provenance.TagCommit != tagCommit {
		t.Fatalf("fallback provenance = %#v", provenance)
	}

	for _, tt := range []struct {
		name      string
		module    moduleVersion
		tagCommit string
		want      string
	}{
		{name: "neither source", module: moduleVersion{Path: callerModule}, want: "caller provenance is unavailable"},
		{name: "ref without hash", module: moduleVersion{Path: callerModule, Origin: &moduleOrigin{Ref: "refs/tags/v0.4.0"}}, want: "caller provenance is unavailable"},
		{name: "invalid fallback", module: moduleVersion{Path: callerModule}, tagCommit: "not-a-commit", want: "tag commit"},
		{name: "invalid origin", module: moduleVersion{Path: callerModule, Origin: &moduleOrigin{Hash: "not-a-hash"}}, want: "origin hash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveCallerProvenance([]moduleVersion{tt.module}, tt.tagCommit)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolveCallerProvenance error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWriteEvidenceRecordsDeclaredAndResolvedTuple(t *testing.T) {
	tuple := boundCompatibilityTuple{
		compatibilityTuple: compatibilityTuple{
			FormatVersion: 1,
			Versions: map[string]string{
				callerModule: "v0.4.0", llmkitModule: "v0.4.0", codexsdkModule: "v0.4.0",
			},
		},
		CheckoutDigest: strings.Repeat("a", 64),
		ProxyDigest:    strings.Repeat("a", 64),
	}
	modules := []moduleVersion{
		{Path: codexsdkModule, Version: "v0.4.0", Sum: "h1:sdk", GoModSum: "h1:sdk-mod"},
		{Path: callerModule, Version: "v0.4.0", Sum: "h1:caller", GoModSum: "h1:caller-mod"},
		{Path: llmkitModule, Version: "v0.4.0", Sum: "h1:kit", GoModSum: "h1:kit-mod"},
	}
	provenance := callerProvenance{
		OriginVCS: "git", OriginURL: "https://github.com/ronhuafeng/llmcaller-codex-go",
		OriginHash: strings.Repeat("d", 40), OriginRef: "refs/tags/v0.4.0",
	}
	var output bytes.Buffer
	if err := writeEvidence(&output, "https://proxy.golang.org", tuple, modules, provenance, []byte(`{"answer":true}`)); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"compatibility_format=1", "checkout_compatibility_sha256=" + strings.Repeat("a", 64),
		"proxy_compatibility_sha256=" + strings.Repeat("a", 64),
		"declared_module=" + callerModule + " declared_version=v0.4.0 resolved_module=" + callerModule + " resolved_version=v0.4.0 sum=h1:caller gomodsum=h1:caller-mod",
		"caller_origin_vcs=git caller_origin_url=https://github.com/ronhuafeng/llmcaller-codex-go caller_origin_hash=" + strings.Repeat("d", 40) + " caller_origin_ref=refs/tags/v0.4.0 caller_tag_commit=",
		"declared_module=" + llmkitModule + " declared_version=v0.4.0 resolved_module=" + llmkitModule + " resolved_version=v0.4.0 sum=h1:kit gomodsum=h1:kit-mod",
		"declared_module=" + codexsdkModule + " declared_version=v0.4.0 resolved_module=" + codexsdkModule + " resolved_version=v0.4.0 sum=h1:sdk gomodsum=h1:sdk-mod",
		`call_evidence={"answer":true}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("evidence missing %q:\n%s", want, text)
		}
	}
	if err := writeEvidence(&bytes.Buffer{}, "https://proxy.golang.org", tuple, modules, callerProvenance{}, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "provenance is unavailable") {
		t.Fatalf("missing provenance write error = %v", err)
	}
	var fallbackOutput bytes.Buffer
	tagCommit := strings.Repeat("e", 40)
	if err := writeEvidence(&fallbackOutput, "https://proxy.golang.org", tuple, modules, callerProvenance{TagCommit: tagCommit}, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fallbackOutput.String(), "caller_origin_hash= caller_origin_ref= caller_tag_commit="+tagCommit) {
		t.Fatalf("fallback provenance evidence missing:\n%s", fallbackOutput.String())
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

func TestPublishedStableTagProxyConsumer(t *testing.T) {
	tag := os.Getenv("LLMCALLER_PROXY_TAG")
	if tag == "" {
		t.Skip("set LLMCALLER_PROXY_TAG to run the public-proxy stable-tag integration")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	compatibilityPath := filepath.Join(repositoryRoot, "compatibility.json")
	contractData, err := os.ReadFile(compatibilityPath)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := decodeCompatibilityTuple(contractData, tag)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	var output bytes.Buffer
	err = run(ctx, config{
		tag:                     tag,
		tagCommit:               os.Getenv("LLMCALLER_PROXY_TAG_COMMIT"),
		compatibilityPath:       compatibilityPath,
		moduleCompatibilityPath: "compatibility.json",
		proxy:                   "https://proxy.golang.org",
		propagationTimeout:      10 * time.Minute,
		retryInterval:           15 * time.Second,
		validationTimeout:       10 * time.Minute,
		commandTimeout:          5 * time.Minute,
		evidenceWriter:          &output,
	})
	if err != nil {
		t.Fatal(err)
	}

	fields := parseEvidenceFields(output.String())
	if fields["proxy"] != "https://proxy.golang.org" || fields["caller_tag"] != tag {
		t.Errorf("proxy/tag evidence = %q / %q", fields["proxy"], fields["caller_tag"])
	}
	checkoutDigest := fields["checkout_compatibility_sha256"]
	if checkoutDigest == "" || fields["proxy_compatibility_sha256"] != checkoutDigest {
		t.Errorf("bound manifest digests = checkout %q proxy %q", checkoutDigest, fields["proxy_compatibility_sha256"])
	}
	for module, version := range expected.Versions {
		line := evidenceLine(output.String(), "declared_module="+module+" ")
		for _, required := range []string{"declared_version=" + version, "resolved_version=" + version, "sum=h1:", "gomodsum=h1:"} {
			if !strings.Contains(line, required) {
				t.Errorf("%s evidence missing %q: %s", module, required, line)
			}
		}
	}
	if fields["caller_origin_hash"] == "" && fields["caller_tag_commit"] == "" {
		t.Error("stable-tag evidence has neither caller origin hash nor checkout tag commit")
	}
	if !strings.Contains(fields["call_evidence"], `"answer":true`) {
		t.Errorf("typed call evidence = %q", fields["call_evidence"])
	}
}

func parseEvidenceFields(evidence string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(evidence, "\n") {
		for _, field := range strings.Fields(line) {
			key, value, ok := strings.Cut(field, "=")
			if ok {
				fields[key] = value
			}
		}
	}
	return fields
}

func evidenceLine(evidence string, prefix string) string {
	for _, line := range strings.Split(evidence, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}
