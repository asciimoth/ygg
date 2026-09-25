package admin

import (
	"encoding/hex"
	"net"
	"slices"
	"strings"

	"github.com/asciimoth/ygg/ygglib/address"
	"github.com/asciimoth/ygg/ygglib/core"
)

type GetPathsRequest struct {
}

type GetPathsResponse struct {
	Paths []PathEntry `json:"paths"`
}

type PathEntry struct {
	IPAddress string   `json:"address"`
	PublicKey string   `json:"key"`
	Path      []uint64 `json:"path"`
	Sequence  uint64   `json:"sequence"`
}

func (a *AdminSocket) getPathsHandler(_ *GetPathsRequest, res *GetPathsResponse) error {
	res.Paths = pathEntries(a.core.GetPaths())
	return nil
}

// pathEntries converts core debug data to its admin representation. Entries
// without a complete public key are omitted because no address can be derived.
func pathEntries(paths []core.PathEntryInfo) []PathEntry {
	entries := make([]PathEntry, 0, len(paths))
	for _, p := range paths {
		addr := address.AddrForKey(p.Key)
		// Debug data can contain incomplete keys while links are changing. Skip
		// such entries because they cannot be represented as Yggdrasil addresses.
		if addr == nil {
			continue
		}
		entries = append(entries, PathEntry{
			IPAddress: net.IP(addr[:]).String(),
			PublicKey: hex.EncodeToString(p.Key),
			Path:      p.Path,
			Sequence:  p.Sequence,
		})
	}
	slices.SortStableFunc(entries, func(a, b PathEntry) int {
		return strings.Compare(a.PublicKey, b.PublicKey)
	})
	return entries
}
