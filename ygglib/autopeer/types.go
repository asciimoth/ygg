package autopeer

import "github.com/asciimoth/gonnect"

// BuiltinSource identifies the generated built-in public peers document.
const BuiltinSource = "BUILTIN"

// Logger is the minimal logging interface used by this package.
type Logger interface {
	Printf(string, ...interface{})
}

// Network aliases the gonnect network abstraction used for source fetching.
type Network = gonnect.Network

// Document matches the public peers document format.
type Document struct {
	SchemaVersion string          `json:"schema_version"`
	GeneratedAt   string          `json:"generated_at"`
	Sources       DocumentSources `json:"sources"`
	Peers         []Peer          `json:"peers"`
}

// DocumentSources describes the origin metadata embedded in a peers document.
type DocumentSources struct {
	PublicPeersRepo   string `json:"public_peers_repo"`
	PublicPeersBranch string `json:"public_peers_branch"`
	UptimePage        string `json:"uptime_page"`
}

// Peer describes one public peer entry from a source document.
type Peer struct {
	Source     string     `json:"-"`
	Continent  string     `json:"continent"`
	Country    string     `json:"country"`
	SourceFile string     `json:"source_file"`
	Label      string     `json:"label,omitempty"`
	Endpoints  []Endpoint `json:"endpoints"`
}

// Endpoint describes one connection endpoint for a public peer.
type Endpoint struct {
	URL         string   `json:"url"`
	Protocol    string   `json:"protocol"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Query       string   `json:"query,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	Normalized  bool     `json:"normalized"`
	Annotations []string `json:"annotations,omitempty"`
	Uptime      *Uptime  `json:"uptime,omitempty"`
}

// Uptime describes the optional public uptime metadata for an endpoint.
type Uptime struct {
	Status          string  `json:"status"`
	StatusClass     string  `json:"status_class"`
	StatusSince     string  `json:"status_since"`
	Uptime7DPercent float64 `json:"uptime_7d_percent"`
	Uptime7DRaw     string  `json:"uptime_7d_raw"`
	ObservedCountry string  `json:"observed_country"`
	ObservedAddress string  `json:"observed_address"`
	ObservedAt      string  `json:"observed_at"`
}
