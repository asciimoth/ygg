module github.com/asciimoth/ygg/yggd

go 1.25.5

require (
	github.com/asciimoth/gonnect v0.15.0
	github.com/asciimoth/mnlib v0.2.3
	github.com/asciimoth/tuntap v0.3.4
	github.com/asciimoth/ygg/ygglib v0.0.0
	github.com/coder/websocket v1.8.14
	github.com/hashicorp/go-syslog v1.0.0
	github.com/hjson/hjson-go/v4 v4.6.0
	github.com/kardianos/minwinsvc v1.0.2
	github.com/miekg/dns v1.1.72
	github.com/olekukonko/tablewriter v1.1.3
	github.com/quic-go/quic-go v0.59.0
	github.com/vishvananda/netlink v1.3.1
	golang.org/x/sys v0.44.0
	golang.org/x/text v0.37.0
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2
	golang.zx2c4.com/wireguard/windows v0.5.3
	suah.dev/protect v1.2.4
)

require (
	github.com/Arceliar/ironwood v0.0.0-20260117132459-7017dbc41d8e // indirect
	github.com/Arceliar/phony v0.0.0-20220903101357-530938a4b13d // indirect
	github.com/asciimoth/bufpool v0.3.0 // indirect
	github.com/asciimoth/gonnect-netstack v0.4.17 // indirect
	github.com/asciimoth/ident v0.2.0 // indirect
	github.com/asciimoth/socksgo v0.2.13 // indirect
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/bits-and-blooms/bloom/v3 v3.7.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/displaywidth v0.10.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.20 // indirect
	github.com/olekukonko/cat v0.0.0-20250911104152-50322a0618f6 // indirect
	github.com/olekukonko/errors v1.2.0 // indirect
	github.com/olekukonko/ll v0.1.6 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/xtaci/smux v1.5.44 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.12.0 // indirect
	golang.org/x/tools v0.44.0 // indirect
	gvisor.dev/gvisor v0.0.0-20260325202830-7644cf3a343c // indirect
)

replace github.com/asciimoth/ygg/ygglib => ../ygglib
