// Package jumper opportunistically adds direct links to remote nodes that
// publish explicit public peering addresses in NodeInfo.
//
// Jumper is intentionally small: it does not run a side service and it does not
// attempt NAT traversal. A node opts in by advertising a NodeInfo value under
// the "jumper" key:
//
//	{
//	  "jumper": {
//	    "addresses": ["tls://example.net:12345"]
//	  }
//	}
//
// When traffic is sent to a remote node, Manager fetches that node's NodeInfo.
// If the remote node published jumper addresses and is not already directly
// connected, Manager adds at most one of those addresses as a persistent peer.
// If that link does not come up within the configured timeout, Manager removes
// only the link it added and tries the next published address on a later check.
package jumper
