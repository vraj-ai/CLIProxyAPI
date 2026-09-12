# Fleet Plugin

vraj's fleet plugin: injects caveman + ponytail instructions into every
routed request, and serves a `/savings` management resource aggregating
pxpipe + pi-subagents compression telemetry.

## Capabilities

- `request_interceptor` — prepends caveman/ponytail text to the request's
  system context (OpenAI `messages`, Anthropic `system`, Responses
  `instructions` shapes all handled).
- `management_api` — registers Fleet → `/savings`, served at
  `/v0/resource/plugins/fleet/savings` and listed in CPAMC.

## Config

```yaml
plugins:
  enabled: true
  dir: "~/.cli-proxy-api/plugins"
  configs:
    fleet:
      enabled: true
      caveman: true     # terse-output instruction
      ponytail: true    # lazy-solution instruction
      # models: ["or/", "ocg/"]  # optional prefix filter; empty = all
      # pxpipe_events: ~/.pxpipe/events.jsonl      # defaults shown
      # usage_gain: ~/.pi/agent/usage-gain.jsonl
```

## Build (macOS)

```bash
mkdir -p ~/.cli-proxy-api/plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared \
  -o ~/.cli-proxy-api/plugins/darwin/$(go env GOARCH)/fleet.dylib \
  ./examples/plugin/fleet/go
rm -f ~/.cli-proxy-api/plugins/darwin/$(go env GOARCH)/fleet.h
```

Then restart: `launchctl kickstart -k gui/$(id -u)/com.vraj.cliproxyapi`.
