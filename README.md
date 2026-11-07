# meetproxy

A Go library and CLI for observing and controlling app windows. Your app chooses the task, target and driver. meetproxy returns the result and an operation receipt.

```text
Your app or CLI → application API → configured driver → result
Your journal → intent → one dispatch → recorded outcome
```

## Compatibility

v0.2 replaces the earlier API and CLI. The CLI provides `control call`, `control result`, `listen`, `listen plist` and `version`. Earlier account, playbook and collection commands aren't included. Keep app guides, account setup and business workflows in your application.

Once v0.2.0 is published, Go module retractions advise against older versions and omit them from automatic version selection. Migrate to the generic API. Retractions don't delete old source or prevent explicit old-version installs.

Listen handlers must accept the generic event fields `source`, `id`, `kind` and `payload`. Slack-specific fields live inside `payload`. Update handlers before switching; acknowledge only after durable acceptance. A matching version label alone doesn't establish compatibility.

## Install

Use Go 1.26.10 or newer. These commands target v0.2.0 once that tag is published:

```sh
go install -ldflags="-X main.version=0.2.0" github.com/jeon-jihyeon/meetproxy/cmd/meetproxy@v0.2.0
meetproxy version
```

Expected version output: `0.2.0`.

For the library:

```sh
go get github.com/jeon-jihyeon/meetproxy@v0.2.0
```

From a source checkout, build locally with `go build -trimpath -ldflags="-X main.version=0.2.0" -o bin/meetproxy ./cmd/meetproxy`.

For Claude Code, install the CLI first and keep `meetproxy` on PATH. Once v0.2.0 is published, add its marketplace and install the plugin from your shell:

```sh
claude plugin marketplace add jeon-jihyeon/meetproxy#v0.2.0
claude plugin install meetproxy@meetproxy
claude plugin list
```

The plugin list should include `meetproxy@meetproxy`. Follow the install summary if a reload is needed, then use `/meetproxy:control`. See the [Claude Code plugin installation guide](https://code.claude.com/docs/en/discover-plugins).

## Configure

- Follow the [Cua Driver quickstart](https://cua.ai/docs/cua-driver/quickstart) to install the driver and grant its OS permissions. Use an already running, trusted daemon.
- The CLI finds `cua-driver` on PATH. Use `--driver` for its executable and `--data` for a private operation directory. Reads don't need an operation ID.
- Writes need a distinct ID and a durable journal. The local journal supports Unix. Go callers can supply their own Driver and Journal with `control.New`.
- Your app owns target ownership and approval. A `completed` result means the driver call completed. An `unknown` result may already have had an effect; inspect its receipt before another action.

## CLI

List apps with one JSON request:

```sh
meetproxy control call <<'JSON'
{"session":"acme","tool":"list_apps","arguments":{}}
JSON
```

A successful response has `tool: "list_apps"`, `state: "completed"`, `recorded: false` and the driver's app list in `output`. App contents depend on the environment.

Window observation and input require the PID and window ID from discovery. Keep the same driver session and use fresh element references. Retrieve a saved write receipt with `meetproxy control result --data <directory> <operation-id>`.

For an owned test document, replace `123` and `456` with its discovered PID and window ID. Observe it first:

```sh
meetproxy control call <<'JSON'
{"session":"acme","tool":"get_window_state","arguments":{"pid":123,"window_id":456,"include_screenshot":false,"tree_format":"elements"}}
JSON
```

The response's `output` contains the window state. Use the text field's fresh `element_token` in the next call. Replace `s00000001:1` below with that token and use a fresh operation ID for each intended write:

```sh
meetproxy control call --data /absolute/private/acme-operations <<'JSON'
{"id":"acme-edit-1","session":"acme","tool":"type_text","arguments":{"target":{"kind":"window","pid":123,"window_id":456},"element_token":"s00000001:1","text":"acme"}}
JSON
```

A successful call returns `state: "completed"` and `recorded: true`. Observe the same window again to check the actual text. If the outcome is `unknown`, retrieve the receipt before deciding how to recover.

See Cua's [window arguments](https://cua.ai/docs/cua-driver/reference/mcp-tools/apps-windows-and-screen#get_window_state) and [input arguments](https://cua.ai/docs/cua-driver/reference/mcp-tools/click-and-type#type_text). For the installed driver's exact `input_schema`, run `cua-driver describe get_window_state` or `cua-driver describe type_text`. meetproxy requires explicit window targets for observation and input.

## Slack events

`listen` forwards supported app mentions and DMs to your receipt handler. It acknowledges each Socket Mode event after the handler confirms durable receipt.

1. Install your Slack app with a bot user. Give it the bot scopes [`app_mentions:read`](https://docs.slack.dev/reference/scopes/app_mentions.read) and [`im:history`](https://docs.slack.dev/reference/events/message.im), then subscribe to `app_mention` and `message.im` in Event Subscriptions. Reinstall the app after changing scopes.
2. Enable [Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/) and create an app-level token with [`connections:write`](https://docs.slack.dev/reference/scopes/connections.write). Store it in a user-owned file with mode `0600` inside a user-owned directory with mode `0700`.
3. Provide your own receipt handler. It reads the complete [Event](https://pkg.go.dev/github.com/jeon-jihyeon/meetproxy/app/listen#Event) JSON from stdin, commits it to durable storage using the pair of `source` and `id` for idempotency and returns exit code `0` only after receipt is confirmed. Repeated identities must confirm the same stored event. A nonzero exit prevents the ACK. The default handler deadline is 1.5 seconds.

Replace the absolute paths and `U1` with your listener directory, token file, handler executable and bot user ID. The listener directory must also be user-owned with mode `0700`:

```sh
meetproxy listen \
  --data /absolute/private/acme-listener \
  --token-file /absolute/private/acme-token/app-token \
  --bot-user U1 \
  --handler /absolute/path/to/acme-receipt-handler
```

Invite the app to your test channel and mention it, or send it a DM. The handler receives one JSON event per invocation; the listener stays running without stdout output until stopped. Message text is data for your application. Your application owns any response or follow-up action.

Go callers can supply any acknowledged event source through `listen.Subscription`. Call `Next` to receive a `Delivery`, persist its `Event`, then call `Ack` with its opaque `Receipt`. Call `Close` to release the subscription. `Next` doesn't acknowledge delivered events. The common event has `source`, `id`, `kind` and `payload`; Slack fields stay inside its payload. Receipts identify a delivery on one connection, so keep them out of your durable event identity and receipt-handler JSON.

## Verified scope

Historical live app checks covered the CLI on macOS with Cua Driver 0.37.0: reading one owned text document, typing into another, observing the result and recovering its receipt from a separate process. The Go API example has a compile check, and local helper-process tests exercised the Go API with a Unix file journal. Claude Code 2.1.290 installed the plugin from a local marketplace in an isolated configuration, listed it as enabled and discovered its `control` skill. Go API control of a real app, model-directed skill selection and execution, Linux desktop control and a live Slack receipt round trip haven't been verified.

## Go API

Import `github.com/jeon-jihyeon/meetproxy/app/control`. Resolve the executable in your application and configure the default adapter explicitly. It requires an absolute executable path and doesn't inherit the CLI's environment. `Open` still supports receipt lookup without an installed driver; an unresolved executable fails at call time:

```go
binary, err := exec.LookPath("cua-driver")
if err != nil {
    return err
}
binary, err = filepath.Abs(binary)
if err != nil {
    return err
}
client, err := control.Open(control.Config{
    Binary: binary,
    Environment: []string{
        "HOME=" + os.Getenv("HOME"),
        "PATH=" + os.Getenv("PATH"),
        "CUA_DRIVER_PERMISSION_MODE=standard",
        "CUA_DRIVER_RS_TELEMETRY_ENABLED=0",
    },
})
if err != nil {
    return err
}
result, err := client.Execute(ctx, control.Request{
    Session: "acme", Tool: "list_apps", Arguments: json.RawMessage(`{}`),
})
if err != nil {
    return err
}
fmt.Println(result.State)
```

Use the standard `context`, `encoding/json`, `fmt`, `os`, `os/exec` and `path/filepath` packages for the surrounding application. Expected output on a successful driver call: `completed`. Provide `Config.Data` or `Config.Journal` before writes.
