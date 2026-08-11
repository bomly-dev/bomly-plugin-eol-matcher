package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	sdk "github.com/bomly-dev/bomly-sdk"
	"github.com/bomly-dev/bomly-sdk/conformance"
	"go.uber.org/zap"
)

// testHost is a minimal HostContext for unit tests.
type testHost struct {
	config json.RawMessage
}

func (h testHost) Logger() *zap.Logger                 { return zap.NewNop() }
func (h testHost) HTTPClient() *sdk.HTTPClientProvider { return nil }
func (h testHost) Runtime() sdk.RuntimeInfo {
	return sdk.RuntimeInfo{Execution: sdk.ExecutionEmbedded}
}

func (h testHost) DecodeConfig(v any) error {
	payload := h.config
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	return json.Unmarshal(payload, v)
}

func newMatcher(t *testing.T, config json.RawMessage) sdk.Matcher {
	t.Helper()
	matcher, err := Module().Matcher.New(context.Background(), testHost{config: config})
	if err != nil {
		t.Fatalf("construct matcher: %v", err)
	}
	return matcher
}

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

	cfg := `{"api_base":"` + server.URL + `/api","cache_dir":"` + filepath.ToSlash(filepath.Join(t.TempDir(), "cache")) + `"}`
	matcher := newMatcher(t, json.RawMessage(cfg))

	registry := sdk.NewPackageRegistry()
	registry.Add(&sdk.Package{
		Coordinates: sdk.Coordinates{
			PURL:      "pkg:pypi/django@4.2.9",
			Name:      "django",
			Version:   "4.2.9",
			Ecosystem: sdk.EcosystemPython,
		},
	})
	resp, err := matcher.Match(context.Background(), sdk.MatchRequest{Registry: registry})
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
	if resp.MatcherStats.Name != Name || resp.MatcherStats.MatchedPackages != 1 {
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

// A config block that does not decode must surface through Ready as the
// not-ready reason instead of failing construction, so the host can report
// it (the ReadyResponse.Reason contract from the legacy serving style).
func TestInvalidConfigSurfacesThroughReady(t *testing.T) {
	matcher := newMatcher(t, json.RawMessage(`{"api_base":42}`))
	err := matcher.Ready(context.Background(), sdk.MatchRequest{})
	if err == nil {
		t.Fatal("expected Ready to report the invalid configuration")
	}
	if _, matchErr := matcher.Match(context.Background(), sdk.MatchRequest{Registry: sdk.NewPackageRegistry()}); matchErr == nil {
		t.Fatal("expected Match to refuse to run with an invalid configuration")
	}
}

// The descriptor's ConfigSchema is generated from the config struct, so a
// renamed or retyped field would silently change the advertised schema. Pin
// the property names and types the plugin actually decodes.
func TestDescriptorConfigSchema(t *testing.T) {
	descriptor := Module().Matcher.Descriptor
	if len(descriptor.ConfigSchema) == 0 {
		t.Fatal("descriptor ConfigSchema is empty")
	}

	var schema struct {
		Type                 string                    `json:"type"`
		Properties           map[string]map[string]any `json:"properties"`
		AdditionalProperties *bool                     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(descriptor.ConfigSchema, &schema); err != nil {
		t.Fatalf("ConfigSchema is not valid JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("expected object schema, got %q", schema.Type)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatal("expected additionalProperties to be false")
	}

	expected := map[string]string{
		"api_base":      "string",
		"cache_dir":     "string",
		"cache_ttl":     "string",
		"timeout":       "string",
		"disable_cache": "boolean",
	}
	if len(schema.Properties) != len(expected) {
		t.Fatalf("expected %d properties, got %#v", len(expected), schema.Properties)
	}
	for name, wantType := range expected {
		property, ok := schema.Properties[name]
		if !ok {
			t.Errorf("missing property %q", name)
			continue
		}
		if property["type"] != wantType {
			t.Errorf("property %q has type %#v, want %q", name, property["type"], wantType)
		}
	}
}

// TestConformance runs the SDK conformance suite against the module,
// including the bomly-plugin.json identity cross-check.
func TestConformance(t *testing.T) {
	conformance.Test(t, conformance.Config{
		Module:       Module(),
		ManifestPath: filepath.Join("..", "bomly-plugin.json"),
		SampleConfig: json.RawMessage(`{"api_base":"https://endoflife.date/api","cache_ttl":"12h","timeout":"10s","disable_cache":true}`),
	})
}

// TestProbeBinary builds the real plugin binary and probes it over the
// managed HashiCorp gRPC transport, asserting the served descriptor equals
// the in-process one.
func TestProbeBinary(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not available; skipping managed-transport probe")
	}
	binaryPath := filepath.Join(t.TempDir(), "bomly-plugin-eol-matcher")
	build := exec.Command(goBinary, "build", "-o", binaryPath, "./cmd/bomly-plugin-eol-matcher")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin binary: %v\n%s", err, output)
	}
	conformance.ProbeBinary(t, binaryPath, conformance.WithModule(Module()))
}
