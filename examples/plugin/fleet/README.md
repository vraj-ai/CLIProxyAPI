# Fleet Plugin

vraj's fleet plugin: injects caveman + ponytail instructions into every
routed request, serves the fleet switchboard (hub, keys, savings, router),
and issues OpenRouter-style API keys for the models this proxy serves.

## Capabilities

- `request_interceptor` — prepends caveman/ponytail text to the request's
  system context (OpenAI `messages`, Anthropic `system`, Responses
  `instructions` shapes all handled). Terminates completions whose model
  is outside the calling key's grant, and 404s unusable ocg muse-spark ids.
- `response_interceptor` — filters `/v1/models` to the calling key's grant
  and drops unusable compatibility ids.
- `management_api` — registers Fleet Hub, Keys, Savings, and Router. Keys
  JSON is key-gated at `/v0/management/fleet/keys`. The Keys HTML shell is
  `/v0/resource/plugins/fleet/keys`. Grants live in
  `~/.cli-proxy-api/fleet-keys.json` (mode 0600). The secret is returned
  only on create.

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
