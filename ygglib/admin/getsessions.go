package admin

import (
	"encoding/hex"
	"net"
	"slices"
	"strings"

	"github.com/asciimoth/ygg/ygglib/address"
	"github.com/asciimoth/ygg/ygglib/core"
)

type GetSessionsRequest struct{}

type GetSessionsResponse struct {
	Sessions []SessionEntry `json:"sessions"`
}

type SessionEntry struct {
	IPAddress string   `json:"address"`
	PublicKey string   `json:"key"`
	RXBytes   DataUnit `json:"bytes_recvd"`
	TXBytes   DataUnit `json:"bytes_sent"`
	Uptime    float64  `json:"uptime"`
}

func (a *AdminSocket) getSessionsHandler(_ *GetSessionsRequest, res *GetSessionsResponse) error {
	res.Sessions = sessionEntries(a.core.GetSessions())
	return nil
}

// sessionEntries converts core debug data to its admin representation. Entries
// without a complete public key are omitted because no address can be derived.
func sessionEntries(sessions []core.SessionInfo) []SessionEntry {
	entries := make([]SessionEntry, 0, len(sessions))
	for _, s := range sessions {
		addr := address.AddrForKey(s.Key)
		// Ignore incomplete debug entries instead of dereferencing a nil address.
		if addr == nil {
			continue
		}
		entries = append(entries, SessionEntry{
			IPAddress: net.IP(addr[:]).String(),
			PublicKey: hex.EncodeToString(s.Key[:]),
			RXBytes:   DataUnit(s.RXBytes),
			TXBytes:   DataUnit(s.TXBytes),
			Uptime:    s.Uptime.Seconds(),
		})
	}
	slices.SortStableFunc(entries, func(a, b SessionEntry) int {
		return strings.Compare(a.PublicKey, b.PublicKey)
	})
	return entries
}
