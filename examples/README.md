# Examples

## `http_example`

`http_example` builds two in-process Yggdrasil nodes, attaches one VTun to each
node, starts an HTTP server on the first node and performs a client request
from the second node.

Run it with:

```sh
go run ./examples/http_example
```

Select the transport network with `-transport-network`:

```sh
go run ./examples/http_example -transport-network=native
go run ./examples/http_example -transport-network=loopback
```

The examples are covered by tests. Run them with:

```sh
go test ./examples/... --race
```

The browser counterpart lives in `../web`. It uses the same Core + VTun pattern
as `http_example`, but replaces the native/loopback carrier network with
browser WebSocket dialing and exposes the HTTP client path in a web UI.
