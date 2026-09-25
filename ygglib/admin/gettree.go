package admin

import (
	"encoding/hex"
	"net"
	"slices"
	"strings"

	"github.com/asciimoth/ygg/ygglib/address"
	"github.com/asciimoth/ygg/ygglib/core"
)

type GetTreeRequest struct{}

type GetTreeResponse struct {
	Tree []TreeEntry `json:"tree"`
}

type TreeEntry struct {
	IPAddress string `json:"address"`
	PublicKey string `json:"key"`
	Parent    string `json:"parent"`
	Sequence  uint64 `json:"sequence"`
}

func (a *AdminSocket) getTreeHandler(_ *GetTreeRequest, res *GetTreeResponse) error {
	res.Tree = treeEntries(a.core.GetTree())
	return nil
}

// treeEntries converts core debug data to its admin representation. Entries
// without a complete public key are omitted because no address can be derived.
func treeEntries(tree []core.TreeEntryInfo) []TreeEntry {
	entries := make([]TreeEntry, 0, len(tree))
	for _, d := range tree {
		addr := address.AddrForKey(d.Key)
		// Ignore incomplete debug entries instead of dereferencing a nil address.
		if addr == nil {
			continue
		}
		entries = append(entries, TreeEntry{
			IPAddress: net.IP(addr[:]).String(),
			PublicKey: hex.EncodeToString(d.Key[:]),
			Parent:    hex.EncodeToString(d.Parent[:]),
			Sequence:  d.Sequence,
		})
	}
	slices.SortStableFunc(entries, func(a, b TreeEntry) int {
		return strings.Compare(a.PublicKey, b.PublicKey)
	})
	return entries
}
