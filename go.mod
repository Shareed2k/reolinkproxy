module github.com/shareed2k/reolinkproxy

go 1.24.0

replace github.com/bluenviron/gortsplib/v4 => ./third_party_gortsplib

require (
	github.com/bluenviron/gortsplib/v4 v4.12.3
	github.com/bluenviron/mediacommon v1.14.0
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/google/uuid v1.6.0
	github.com/pion/rtp v1.10.1
	github.com/urfave/cli/v3 v3.8.0
	go.uber.org/zap v1.27.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.15 // indirect
	github.com/pion/sdp/v3 v3.0.10 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/net v0.44.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/sys v0.36.0 // indirect
)
