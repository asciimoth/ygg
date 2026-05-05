package autopeer

import _ "embed"

//go:embed builtin_peers_generated.json
var builtinPeersJSON []byte
