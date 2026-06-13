package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bomly-dev/bomly-cli/sdk"
)

func TestMatchEnrichesPackageMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/all.json":
			_ = json.NewEncoder(w).Encode([]string{"django"})
		case "/api/django.json":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"cycle":   "4.2",
				"eol":     "2030-01-01",
				"support": false,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "plugin.json")
	cfg := `{"api_base":"` + server.URL + `/api","cache_dir":"` + filepath.ToSlash(filepath.Join(configDir, "cache")) + `"}`
	if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(sdk.EnvPluginConfigFile, configPath)

	registry := sdk.NewPackageRegistry()
	registry.Add(&sdk.Package{
		Coordinates: sdk.Coordinates{
			PURL:      "pkg:pypi/django@4.2.9",
			Name:      "django",
			Version:   "4.2.9",
			Ecosystem: sdk.EcosystemPython,
		},
	})
	resp, err := (&matcher{}).Match(context.Background(), &sdk.MatchRequest{Registry: registry})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	pkg, ok := resp.Registry.Get("pkg:pypi/django@4.2.9")
	if !ok {
		t.Fatal("package missing")
	}
	got, ok := pkg.Metadata[metadataEOLKey].(map[string]any)
	if !ok {
		t.Fatalf("expected eol metadata map, got %#v", pkg.Metadata[metadataEOLKey])
	}
	if got["status"] != statusSupported {
		t.Fatalf("expected status %q, got %#v", statusSupported, got["status"])
	}
	if got["cycle"] != "4.2" {
		t.Fatalf("expected cycle 4.2, got %#v", got["cycle"])
	}
	if resp.MatcherStats.Name != matcherName || resp.MatcherStats.MatchedPackages != 1 {
		t.Fatalf("matcher stats = %#v", resp.MatcherStats)
	}
}

func TestFetchProductsUsesCacheAfterFirstRequest(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_ = json.NewEncoder(w).Encode([]string{"django"})
	}))
	defer server.Close()

	cache := newFileCache(filepath.Join(t.TempDir(), "cache"), "1h", false)
	client := server.Client()
	if _, err := fetchProducts(context.Background(), client, server.URL, cache); err != nil {
		t.Fatalf("fetchProducts() error = %v", err)
	}
	if _, err := fetchProducts(context.Background(), client, server.URL, cache); err != nil {
		t.Fatalf("fetchProducts() second call error = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected one HTTP request due to cache, got %d", requestCount)
	}
}

func TestMatchCycleFallback(t *testing.T) {
	cycles := []productCycle{{Cycle: "1", EOL: dateOrBool{Date: "2030-01-01"}}}
	matched, ok := matchCycle("1.2.3", cycles)
	if !ok {
		t.Fatal("expected cycle match")
	}
	if matched.Cycle != "1" {
		t.Fatalf("expected cycle 1, got %q", matched.Cycle)
	}
}
