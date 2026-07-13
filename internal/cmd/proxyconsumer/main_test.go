package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateResolvedModulesRequiresExactCallerAndStableUpstreams(t *testing.T) {
	modules := []moduleVersion{
		{Path: callerModule, Version: "v0.4.0", Sum: "h1:caller", GoModSum: "h1:caller-mod"},
		{Path: llmkitModule, Version: "v0.4.0", Sum: "h1:kit", GoModSum: "h1:kit-mod"},
		{Path: codexsdkModule, Version: "v0.4.0", Sum: "h1:sdk", GoModSum: "h1:sdk-mod"},
	}
	if err := validateResolvedModules(modules, "v0.4.0"); err != nil {
		t.Fatal(err)
	}

	bad := append([]moduleVersion(nil), modules...)
	bad[0].Version = "v0.4.1"
	if err := validateResolvedModules(bad, "v0.4.0"); err == nil || !strings.Contains(err.Error(), "resolved caller") {
		t.Fatalf("caller mismatch error = %v", err)
	}
	bad = append([]moduleVersion(nil), modules...)
	bad[1].Version = "v0.0.0-20260713000000-deadbeefdead"
	if err := validateResolvedModules(bad, "v0.4.0"); err == nil || !strings.Contains(err.Error(), llmkitModule) {
		t.Fatalf("pseudo-version error = %v", err)
	}
	bad = append([]moduleVersion(nil), modules...)
	bad[2].Sum = ""
	if err := validateResolvedModules(bad, "v0.4.0"); err == nil || !strings.Contains(err.Error(), "module sum") {
		t.Fatalf("missing sum error = %v", err)
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
