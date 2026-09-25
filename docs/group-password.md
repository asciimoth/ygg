# Group-password authentication

`core.GroupPassword` and the daemon `GroupPassword` setting can restrict
end-to-end overlay sessions to nodes that use the same password. The empty
default keeps compatibility with the public overlay. A node that sets a group
password cannot open overlay sessions with public services or with other nodes
that use no password or a different password.

Use a strong, high-entropy password. The Ironwood handshake construction is not
a slow password hash. An attacker that records suitable handshake traffic can
try weak passwords offline.

This feature is not a firewall. It authenticates end-to-end overlay sessions,
but it does not:

- restrict or hide direct peer connections;
- stop a node from routing transit traffic for another password group; or
- stop group traffic from passing through routing nodes that are not members
  of the group.

All group members must protect the shared password. Use normal host and network
controls when you must restrict peer links, listening services, or traffic
exposure.

## Library use

Pass a non-empty option when you create the core:

```go
node, err := core.New(cert, logger,
	core.TransportManager{Manager: manager},
	core.GroupPassword("a-strong-random-secret"),
)
```

Omit `core.GroupPassword`, or pass an empty value, to use the normal public
overlay behavior.

## Daemon configuration

Set the same value on each group member:

```hjson
GroupPassword: "a-strong-random-secret"
```

Generated configurations contain `GroupPassword` with an empty value. Existing
configurations that omit the setting continue to use the public overlay.
