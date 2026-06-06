package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bomly-dev/bomly-cli/sdk"
)

const (
	statusSupported      = "supported"
	statusSecurityOnly   = "security-only"
	statusApproachingEOL = "approaching-eol"
	statusEndOfLife      = "end-of-life"
	statusUnknown        = "unknown"

	metadataEOLKey = "endoflife.date"
	matcherName    = "eol-lifecycle-matcher"
	displayName    = "endoflife.date Lifecycle Matcher"
	pluginVersion  = "0.1.0"

	defaultAPIBase  = "https://endoflife.date/api"
	defaultCacheTTL = 24 * time.Hour
	defaultTimeout  = 15 * time.Second

	approachingWindowDays = 180
)

type matcher struct{}

type config struct {
	APIBase      string `json:"api_base"`
	CacheDir     string `json:"cache_dir"`
	CacheTTL     string `json:"cache_ttl"`
	Timeout      string `json:"timeout"`
	DisableCache bool   `json:"disable_cache"`
}

func (m *matcher) Metadata(context.Context) (*sdk.PluginMetadata, error) {
	return &sdk.PluginMetadata{
		ID:               matcherName,
		Name:             displayName,
		Version:          pluginVersion,
		Kind:             sdk.PluginKindMatcher,
		PluginAPIVersion: sdk.PluginAPIVersion,
		Description:      "External matcher plugin that enriches packages with endoflife.date lifecycle metadata.",
		Homepage:         "https://github.com/bomly-dev/bomly-plugin-eol-lifecycle",
		License:          "Apache-2.0",
	}, nil
}

func (m *matcher) Descriptor(context.Context) (*sdk.MatcherDescriptor, error) {
	return &sdk.MatcherDescriptor{
		Name:         matcherName,
		DisplayName:  displayName,
		Aliases:      []string{"eol"},
		Enabled:      false,
		Origin:       sdk.ExternalOrigin,
		Priority:     80,
		Required:     false,
		Capabilities: []string{"lifecycle-enrichment", "http", "cache"},
	}, nil
}

func (m *matcher) Ready(context.Context, *sdk.MatchRequest) (*sdk.ReadyResponse, error) {
	_, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return &sdk.ReadyResponse{Ready: true}, nil
}

func (m *matcher) Applicable(_ context.Context, req *sdk.MatchRequest) (*sdk.ApplicableResponse, error) {
	return &sdk.ApplicableResponse{Applicable: req != nil && req.Registry != nil}, nil
}

func (m *matcher) Match(ctx context.Context, req *sdk.MatchRequest) (*sdk.MatchResponse, error) {
	if req == nil || req.Registry == nil {
		return matchResponse(nil, 0), nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	timeout := parseDurationOrDefault(cfg.Timeout, defaultTimeout)
	client, err := httpClient(timeout)
	if err != nil {
		return nil, err
	}
	cache := newFileCache(cfg.CacheDir, cfg.CacheTTL, cfg.DisableCache)

	products, err := fetchProducts(ctx, client, cfg.APIBase, cache)
	if err != nil {
		return matchResponse(req.Registry, 0), err
	}

	enrichedCount := 0
	for _, pkg := range req.Registry.All() {
		if pkg == nil || strings.TrimSpace(pkg.Version) == "" {
			continue
		}
		product, ok := resolveProduct(pkg, products)
		if !ok {
			continue
		}
		cycles, err := fetchCycles(ctx, client, cfg.APIBase, cache, product)
		if err != nil {
			continue
		}
		entry := classifyEOL(product, strings.TrimSpace(pkg.Version), cycles, time.Now().UTC())
		if entry == nil {
			continue
		}
		if pkg.Metadata == nil {
			pkg.Metadata = make(map[string]any, 1)
		}
		pkg.Metadata[metadataEOLKey] = entry
		pkg.Matched = true
		enrichedCount++
	}
	return matchResponse(req.Registry, enrichedCount), nil
}

func matchResponse(registry *sdk.PackageRegistry, matchedPackages int) *sdk.MatchResponse {
	return &sdk.MatchResponse{
		Registry:    registry,
		MatcherRuns: []string{matcherName},
		MatcherRunDetails: []sdk.MatcherRun{{
			Name:            matcherName,
			DisplayName:     displayName,
			MatchedPackages: matchedPackages,
		}},
	}
}

func loadConfig() (config, error) {
	cfg := config{
		APIBase:  defaultAPIBase,
		CacheDir: defaultCacheDir(),
		CacheTTL: defaultCacheTTL.String(),
		Timeout:  defaultTimeout.String(),
	}
	if err := sdk.DecodePluginConfigFromEnv(&cfg); err != nil {
		return config{}, err
	}
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = defaultAPIBase
	}
	if strings.TrimSpace(cfg.CacheDir) == "" {
		cfg.CacheDir = defaultCacheDir()
	}
	if strings.TrimSpace(cfg.CacheTTL) == "" {
		cfg.CacheTTL = defaultCacheTTL.String()
	}
	if strings.TrimSpace(cfg.Timeout) == "" {
		cfg.Timeout = defaultTimeout.String()
	}
	return cfg, nil
}

func defaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".bomly-cache", "eol")
	}
	return filepath.Join(home, ".bomly", "cache", "eol")
}

func httpClient(timeout time.Duration) (*http.Client, error) {
	provider, err := sdk.NewHTTPClientProviderFromEnv()
	if err != nil {
		return nil, err
	}
	return provider.Client(timeout), nil
}

func parseDurationOrDefault(value string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func fetchProducts(ctx context.Context, client *http.Client, apiBase string, cache fileCache) (map[string]struct{}, error) {
	if cached, ok := cache.getStrings("products"); ok {
		return productSet(cached), nil
	}

	endpoint := strings.TrimRight(apiBase, "/") + "/all.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("eol: build product list request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eol: fetch product list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("eol: product list request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var products []string
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		return nil, fmt.Errorf("eol: decode product list: %w", err)
	}
	_ = cache.set("products", products)
	return productSet(products), nil
}

func productSet(products []string) map[string]struct{} {
	set := make(map[string]struct{}, len(products))
	for _, item := range products {
		set[strings.ToLower(strings.TrimSpace(item))] = struct{}{}
	}
	return set
}

type dateOrBool struct {
	Date string
	Bool *bool
}

func (d *dateOrBool) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		d.Date = ""
		d.Bool = nil
		return nil
	}
	if trimmed == "true" || trimmed == "false" {
		value := trimmed == "true"
		d.Bool = &value
		d.Date = ""
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return err
	}
	d.Date = strings.TrimSpace(asString)
	d.Bool = nil
	return nil
}

func (d *dateOrBool) reached(now time.Time) bool {
	if d.Bool != nil {
		return *d.Bool
	}
	if strings.TrimSpace(d.Date) == "" {
		return false
	}
	date, err := time.Parse("2006-01-02", d.Date)
	if err != nil {
		return false
	}
	return !date.After(now)
}

type productCycle struct {
	Cycle   string      `json:"cycle"`
	EOL     dateOrBool  `json:"eol"`
	Support *dateOrBool `json:"support,omitempty"`
	LTS     *dateOrBool `json:"lts,omitempty"`
}

func fetchCycles(ctx context.Context, client *http.Client, apiBase string, cache fileCache, product string) ([]productCycle, error) {
	key := "cycles/" + product
	if cached, ok := cache.getCycles(key); ok {
		return cached, nil
	}

	endpoint := strings.TrimRight(apiBase, "/") + "/" + url.PathEscape(product) + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("eol: build cycle request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eol: fetch cycles for %s: %w", product, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("eol: cycle request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var cycles []productCycle
	if err := json.NewDecoder(resp.Body).Decode(&cycles); err != nil {
		return nil, fmt.Errorf("eol: decode cycles for %s: %w", product, err)
	}
	_ = cache.set(key, cycles)
	return cycles, nil
}

func resolveProduct(pkg *sdk.Package, products map[string]struct{}) (string, bool) {
	if pkg == nil || len(products) == 0 {
		return "", false
	}
	eco := strings.ToLower(strings.TrimSpace(pkg.Ecosystem))
	name := strings.ToLower(strings.TrimSpace(pkg.Name))
	org := strings.ToLower(strings.TrimSpace(pkg.Org))

	candidates := make([]string, 0, 4)
	switch eco {
	case "npm":
		if org == "angular" && name == "core" {
			candidates = append(candidates, "angular")
		}
		candidates = append(candidates, name)
	case "python":
		candidates = append(candidates, strings.ReplaceAll(name, "_", "-"), name)
	case "go", "golang":
		parts := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
		if len(parts) > 0 {
			candidates = append(candidates, parts[len(parts)-1])
		}
	case "maven":
		if org == "org.springframework.boot" && name == "spring-boot" {
			candidates = append(candidates, "spring-boot")
		}
		candidates = append(candidates, name)
	default:
		candidates = append(candidates, name)
	}

	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if _, ok := products[candidate]; ok {
			return candidate, true
		}
	}
	return "", false
}

func classifyEOL(product, version string, cycles []productCycle, now time.Time) map[string]any {
	cycle, ok := matchCycle(version, cycles)
	if !ok {
		return map[string]any{"product": product, "status": statusUnknown}
	}

	status := statusSupported
	result := map[string]any{"product": product, "cycle": cycle.Cycle}

	if cycle.EOL.reached(now) {
		status = statusEndOfLife
	} else if cycle.Support != nil && cycle.Support.reached(now) {
		status = statusSecurityOnly
	} else if strings.TrimSpace(cycle.EOL.Date) != "" {
		eolDate, err := time.Parse("2006-01-02", cycle.EOL.Date)
		if err == nil {
			days := int(eolDate.Sub(now).Hours() / 24)
			result["eol_date"] = cycle.EOL.Date
			result["days_until_eol"] = days
			if days >= 0 && days <= approachingWindowDays {
				status = statusApproachingEOL
			}
		}
	}

	result["status"] = status
	return result
}

func matchCycle(version string, cycles []productCycle) (productCycle, bool) {
	version = strings.TrimSpace(strings.TrimPrefix(version, "v"))
	if version == "" || len(cycles) == 0 {
		return productCycle{}, false
	}
	for _, candidate := range cycleCandidates(version) {
		for _, cycle := range cycles {
			if strings.EqualFold(strings.TrimSpace(cycle.Cycle), candidate) {
				return cycle, true
			}
		}
	}
	return productCycle{}, false
}

func cycleCandidates(version string) []string {
	trimmed := strings.TrimSpace(version)
	parts := strings.Split(trimmed, ".")
	major := numericPrefix(parts[0])
	if major == "" {
		return []string{trimmed}
	}
	candidates := []string{trimmed}
	if len(parts) > 1 {
		minor := numericPrefix(parts[1])
		if minor != "" {
			candidates = append(candidates, major+"."+minor)
		}
	}
	candidates = append(candidates, major)
	return uniqueCandidates(candidates)
}

func numericPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return ""
	}
	if _, err := strconv.Atoi(b.String()); err != nil {
		return ""
	}
	return b.String()
}

func uniqueCandidates(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

type fileCache struct {
	dir      string
	ttl      time.Duration
	disabled bool
}

func newFileCache(dir, ttlText string, disabled bool) fileCache {
	ttl := parseDurationOrDefault(ttlText, defaultCacheTTL)
	return fileCache{dir: dir, ttl: ttl, disabled: disabled}
}

func (c fileCache) getStrings(key string) ([]string, bool) {
	var values []string
	ok := c.get(key, &values)
	return values, ok
}

func (c fileCache) getCycles(key string) ([]productCycle, bool) {
	var values []productCycle
	ok := c.get(key, &values)
	return values, ok
}

func (c fileCache) get(key string, value any) bool {
	if c.disabled || c.dir == "" {
		return false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return false
	}
	var entry struct {
		CreatedAt time.Time       `json:"created_at"`
		Value     json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &entry); err != nil || time.Since(entry.CreatedAt) > c.ttl {
		return false
	}
	return json.Unmarshal(entry.Value, value) == nil
}

func (c fileCache) set(key string, value any) error {
	if c.disabled || c.dir == "" {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	entry := struct {
		CreatedAt time.Time       `json:"created_at"`
		Value     json.RawMessage `json:"value"`
	}{CreatedAt: time.Now().UTC(), Value: raw}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path(key), data, 0o644)
}

func (c fileCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}

func main() {
	sdk.ServeMatcher(&matcher{})
}
