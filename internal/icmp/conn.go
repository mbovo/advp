// Package icmp builds and parses ICMP echo messages and provides ICMP sockets
// (unprivileged datagram "ping sockets" or raw sockets) for Linux and macOS.
package icmp

import (
	"net"
	"time"
)

// Conn is an ICMP socket. It is an interface so callers can be tested
// without network access.
type Conn interface {
	// Send transmits a complete ICMP message and returns the send time.
	Send(pkt []byte) (time.Time, error)
	// Read waits up to timeout for one packet. ok=false with a nil error
	// means nothing relevant was received.
	Read(timeout time.Duration) (ev Event, ok bool, err error)
	// ID is the echo identifier used on the wire.
	ID() uint16
	// FilterID reports whether replies are demultiplexed by ID in user space.
	FilterID() bool
	// Raw reports whether this is a raw socket.
	Raw() bool
	Close() error
}

// Config describes how to open and configure an ICMP socket.
type Config struct {
	V6          bool
	Dst         net.IP
	Zone        string
	TTL         int // -1 = system default
	TOS         int // -1 = unset
	Src         net.IP
	IfName      string
	IfIndex     int
	Mark        int
	MarkSet     bool
	PMTU        string // do, dont, want, probe
	Broadcast   bool
	DontRoute   bool
	Debug       bool
	NoMcastLoop bool
	Connect     bool
	RecordRoute bool
	PreferRaw   bool
	SndBuf      int
	Ident       int // -1 = automatic
	// Warnings collects non-fatal setup problems.
	Warnings *[]string
}

func (c *Config) warn(s string) {
	if c.Warnings != nil {
		*c.Warnings = append(*c.Warnings, s)
	}
}

// HasErrQueue reports whether local send errors are also delivered through
// the socket error queue (Linux IP_RECVERR), where they carry more details.
func HasErrQueue(c Conn) bool {
	q, ok := c.(interface{ HasErrQueue() bool })
	return ok && q.HasErrQueue()
}
