module github.com/7mind/wanbond

go 1.26.4

// v3.1.20260828 (b5928ef) plus local engine patches: send completion,
// outbound admission/statistics, encrypted-flow metadata, and the unmerged
// S4 TUN-read rebase from upstream PR #169. See docs/design.md.
replace github.com/amnezia-vpn/amneziawg-go/v3 => ./third_party/amneziawg-go

require (
	github.com/amnezia-vpn/amneziawg-go/v3 v3.1.20260828
	github.com/pelletier/go-toml/v2 v2.4.2
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2
	github.com/prometheus/common v0.66.1
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.42.0
	golang.org/x/net v0.44.0
	golang.org/x/sys v0.36.0
)

require github.com/coder/websocket v1.8.15

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
)
