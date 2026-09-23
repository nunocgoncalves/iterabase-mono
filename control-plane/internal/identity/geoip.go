package identity

import (
	"fmt"
	"net"

	"github.com/oschwald/maxminddb-golang"
)

// Location is the advisory coarse region attached to a browser session. It is
// presentation metadata only and never authorizes a request.
type Location struct {
	Country string
	Region  string
}

// LocationResolver derives advisory location for a source IP from an
// operator-mounted local database. Implementations never perform outbound
// lookups.
type LocationResolver interface {
	Lookup(ip net.IP) (Location, bool)
}

// NoLocation is the fail-safe resolver used when no local database is mounted.
type NoLocation struct{}

// Lookup always reports no location.
func (NoLocation) Lookup(net.IP) (Location, bool) { return Location{}, false }

// MaxMindLocation reads an operator-mounted MaxMind-format database.
type MaxMindLocation struct {
	reader *maxminddb.Reader
}

type maxMindRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"subdivisions"`
}

// OpenMaxMindLocation opens a local MaxMind database. A malformed or missing
// database is a startup error for the configured path.
func OpenMaxMindLocation(path string) (*MaxMindLocation, error) {
	reader, err := maxminddb.Open(path) //nolint:gosec // operator-configured local path
	if err != nil {
		return nil, fmt.Errorf("opening geoip database: %w", err)
	}
	return &MaxMindLocation{reader: reader}, nil
}

// Lookup returns a bounded country/region projection when available.
func (m *MaxMindLocation) Lookup(ip net.IP) (Location, bool) {
	if m == nil || m.reader == nil || ip == nil {
		return Location{}, false
	}
	var record maxMindRecord
	if err := m.reader.Lookup(ip, &record); err != nil {
		return Location{}, false
	}
	location := Location{Country: boundLabel(record.Country.ISOCode, 64)}
	if len(record.Subdivisions) > 0 {
		location.Region = boundLabel(record.Subdivisions[0].ISOCode, 64)
	}
	if location.Country == "" && location.Region == "" {
		return Location{}, false
	}
	return location, true
}

// Close releases the database handle.
func (m *MaxMindLocation) Close() error {
	if m == nil || m.reader == nil {
		return nil
	}
	return m.reader.Close()
}

func boundLabel(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}
