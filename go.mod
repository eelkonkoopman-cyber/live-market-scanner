module github.com/eelkonkoopman/marketscanner

go 1.26

// Dependencies are added as packages land (go mod tidy will populate require):
//   github.com/coder/websocket   - WS server + client
//   modernc.org/sqlite           - pure-Go SQLite (no cgo)
//   github.com/bytedance/sonic   - fast JSON for the hot ingest path
//   golang.org/x/sync/errgroup   - lifecycle management
//
// The standalone Phase-0 spikes under ./spike and ./spike-flatfiles are their
// own modules and are excluded from this one automatically.

require (
	github.com/anthropics/anthropic-sdk-go v1.58.1
	github.com/coder/websocket v1.8.15
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/scmhub/ibapi v0.10.37 // PINNED: test-only byte-equality oracle for internal/orderticket (twsclient_ibapi_test.go), which mirrors v0.10.37
	golang.org/x/net v0.57.0
	golang.org/x/sys v0.47.0
	modernc.org/sqlite v1.53.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robaho/fixed v0.0.0-20251201003256-beee5759f86a // indirect
	github.com/rs/zerolog v1.34.0 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
