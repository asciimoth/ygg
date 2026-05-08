# Yggdrasil Web VTun Demo

This directory contains a browser WASM demo that starts one Yggdrasil Core node,
attaches a VTun userspace network stack, connects over `wss://` browser-safe
WebSocket transports, runs autopeer from the built-in public-peer list, and lets the UI
issue HTTP requests through VTun.

Build and serve the static web demo:

```sh
just web-serve
```

Then open `http://127.0.0.1:8000/`.

Browsers have no native socket API for Go WASM. The transport manager therefore
uses `gonnect/reject.Network` as its default carrier network. The browser
WebSocket transport ignores that carrier network and calls
`github.com/coder/websocket.Dial`, which uses the host JavaScript WebSocket API
on `GOOS=js GOARCH=wasm`.

The demo defaults to `wss` autopeer endpoints. Plain `ws://` endpoints exist in
the built-in public peer list, but modern browser settings such as Firefox
HTTPS-Only Mode can upgrade or block them before WASM sees the connection.

The demo connects directly to `ws://` and `wss://` peers through the browser
WebSocket API. Browsers always send an `Origin` header on cross-origin
WebSocket handshakes, so the demo can only connect to peers that accept
cross-origin requests from the page hosting the demo.
