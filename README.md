# endoflife.date Lifecycle Matcher Plugin

External Bomly matcher plugin for [endoflife.date](https://endoflife.date) lifecycle metadata. This plugin carries the matcher ID `eol-lifecycle-matcher` and the short selector alias `eol`.

## Build and test

```bash
go test ./...
go build -o bin/bomly-plugin-eol-lifecycle .
```

## Install for local development

```bash
bomly plugin install ./bin/bomly-plugin-eol-lifecycle --dev
bomly plugin enable eol-lifecycle-matcher
bomly scan --enrich --matchers +eol
```

## Install from an archive

```bash
bomly plugin install ./dist/bomly-plugin-eol-lifecycle_linux_amd64.tar.gz
bomly plugin enable eol-lifecycle-matcher
```

Direct URL installs require a checksum unless you explicitly opt out:

```bash
bomly plugin install https://example.internal/bomly-plugin-eol-lifecycle_linux_amd64.tar.gz \
  --checksum sha256:<digest>
```

## Install from a private GitHub Release

```bash
export BOMLY_GITHUB_TOKEN=<token-with-release-access>
bomly plugin install github:bomly-dev/bomly-plugin-eol-lifecycle@v0.1.0
bomly plugin enable eol-lifecycle-matcher
```

`GITHUB_TOKEN`, `GH_TOKEN`, and `GITHUB_AUTH_TOKEN` are also accepted by Bomly for private release metadata and asset downloads.

## Configuration

Configure the plugin in Bomly's plugin config map:

```yaml
plugins:
  eol-lifecycle-matcher:
    api_base: https://endoflife.date/api
    cache_dir: ~/.bomly/cache/eol
    cache_ttl: 24h
    timeout: 15s
    disable_cache: false
```

The plugin honors Bomly's proxy environment passed to external plugins.
