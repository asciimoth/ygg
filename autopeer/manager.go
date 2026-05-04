package autopeer

// Manager is the lifecycle wrapper for public autopeering state.
//
// It currently owns only the fetcher-facing side of autopeering and does not
// interact with core yet.
type Manager struct {
	fetcher *Fetcher
}

// NewManager constructs an autopeering manager around a fetcher.
func NewManager(fetcher *Fetcher) *Manager {
	return &Manager{fetcher: fetcher}
}

// Fetcher returns the underlying public peers fetcher.
func (m *Manager) Fetcher() *Fetcher {
	if m == nil {
		return nil
	}
	return m.fetcher
}

// Start starts the underlying fetcher.
func (m *Manager) Start() bool {
	if m == nil || m.fetcher == nil {
		return false
	}
	return m.fetcher.Start()
}

// Close stops the underlying fetcher.
func (m *Manager) Close() error {
	if m == nil || m.fetcher == nil {
		return nil
	}
	return m.fetcher.Close()
}

// Peers returns the current public peers snapshot.
func (m *Manager) Peers() []Peer {
	if m == nil || m.fetcher == nil {
		return nil
	}
	return m.fetcher.Peers()
}

// SetOnChange installs the fetcher change callback.
func (m *Manager) SetOnChange(fn func([]Peer)) {
	if m == nil || m.fetcher == nil {
		return
	}
	m.fetcher.SetOnChange(fn)
}
