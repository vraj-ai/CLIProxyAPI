# Fleet Plugin

vraj's Fleet plugin: applies model-policy-controlled caveman + ponytail
instructions to requests, exposes Fleet management data, and issues client API
keys for the models this proxy serves. The compatible React management app is
the Fleet interface.

## Capabilities

- `request_interceptor` — prepends caveman/ponytail text to the request's
  system context (OpenAI `messages`, Anthropic `system`, Responses
  `instructions` shapes all handled). Terminates completions whose model
  is outside the calling key's grant, and 404s unusable ocg muse-spark ids.
- `response_interceptor` — filters `/v1/models` to the calling key's grant
  and drops unusable compatibility ids.
- `management_api` — exposes key-gated Fleet state, savings, and key routes
  under `/v0/management/fleet`. Legacy resource URLs redirect to native Fleet
  views in `/management.html`. The management asset is served unchanged.
  Grants live in
  `~/.cli-proxy-api/fleet-keys.json` (mode 0600). The secret is returned
  only on create.

The plugin does not register a synthetic model, executor, or auth provider.
Provider model selection remains in the proxy's normal routing path.

Fleet access controls select an exact served model and let it inherit or
override pxpipe, caveman, ponytail, Headroom, and RTK. Headroom runs as a
separate OpenAI-compatible client proxy; RTK filters agent shell output. They
are not provider-side hops.

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
      client_tools:     # optional client layers; both default false
        headroom: false
        rtk: false
      # model_policies_path: ~/.cli-proxy-api/fleet-policies.json
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
