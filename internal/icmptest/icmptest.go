// Package icmptest provides a simulated icmp.Conn so the ping engine and the
// CLI can be tested without network access or privileges.
package icmptest

import (
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/mbovo/advp/internal/icmp"
)

// Reply decides what the simulated network answers to the echo request with
// sequence number seq: an event delivered after delay, or nothing (ok=false).
type Reply func(seq uint16) (ev icmp.Event, delay time.Duration, ok bool)

// Conn is a simulated ICMP socket. It is safe for concurrent use.
type Conn struct {
	id uint16
	ch chan icmp.Event

	mu       sync.Mutex
	reply    Reply
	sendErr  func(seq uint16) error
	sent     []uint16
	closed   bool
	raw      bool
	filterID bool
}

// New returns a connection with identifier id answering through reply.
func New(id uint16, reply Reply) *Conn {
	return &Conn{id: id, ch: make(chan icmp.Event, 4096), reply: reply, filterID: true}
}

// SetReply replaces the reply function.
func (c *Conn) SetReply(r Reply) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reply = r
}

// SetSendError makes Send fail for the sequence numbers where f returns an error.
func (c *Conn) SetSendError(f func(seq uint16) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendErr = f
}

// SetRaw sets the value reported by Raw.
func (c *Conn) SetRaw(raw bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raw = raw
}

// Inject delivers ev after delay, filling in the identifier, the arrival
// time and TTL -1 when unset. The sequence number is kept as given.
func (c *Conn) Inject(ev icmp.Event, delay time.Duration) {
	ev.ID, ev.At = c.id, time.Now().Add(delay)
	if ev.TTL == 0 {
		ev.TTL = -1
	}
	time.AfterFunc(delay, func() { c.ch <- ev })
}

// Sent returns the sequence numbers sent successfully so far.
func (c *Conn) Sent() []uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uint16(nil), c.sent...)
}

// Closed reports whether Close was called.
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Send implements icmp.Conn.
func (c *Conn) Send(pkt []byte) (time.Time, error) {
	now := time.Now()
	seq := binary.BigEndian.Uint16(pkt[6:])
	c.mu.Lock()
	reply, sendErr := c.reply, c.sendErr
	c.mu.Unlock()
	if sendErr != nil {
		if err := sendErr(seq); err != nil {
			return now, err
		}
	}
	c.mu.Lock()
	c.sent = append(c.sent, seq)
	c.mu.Unlock()
	if reply == nil {
		return now, nil
	}
	if ev, d, ok := reply(seq); ok {
		ev.ID, ev.Seq, ev.At = c.id, seq, now.Add(d)
		if ev.TTL == 0 {
			ev.TTL = -1
		}
		time.AfterFunc(d, func() { c.ch <- ev })
	}
	return now, nil
}

// Read implements icmp.Conn.
func (c *Conn) Read(timeout time.Duration) (icmp.Event, bool, error) {
	select {
	case ev := <-c.ch:
		return ev, true, nil
	case <-time.After(timeout):
		return icmp.Event{}, false, nil
	}
}

// ID implements icmp.Conn.
func (c *Conn) ID() uint16 { return c.id }

// FilterID implements icmp.Conn.
func (c *Conn) FilterID() bool { return c.filterID }

// Raw implements icmp.Conn.
func (c *Conn) Raw() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw
}

// Close implements icmp.Conn.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// EchoReply answers every request from "from" after delay with the given TTL
// (use 0 for "unknown").
func EchoReply(from net.IP, delay time.Duration, ttl int) Reply {
	return func(uint16) (icmp.Event, time.Duration, bool) {
		return icmp.Event{Kind: icmp.KindReply, From: from, Size: 64, TTL: ttl}, delay, true
	}
}

// Silent never answers.
func Silent() Reply {
	return func(uint16) (icmp.Event, time.Duration, bool) { return icmp.Event{}, 0, false }
}

// TimeExceeded answers like a router "from" (IPv4 Time Exceeded) after delay.
func TimeExceeded(from net.IP, delay time.Duration) Reply {
	return func(uint16) (icmp.Event, time.Duration, bool) {
		return icmp.Event{Kind: icmp.KindError, Type: icmp.V4TimeExceeded, From: from, Size: 36}, delay, true
	}
}
