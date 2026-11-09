//go:build darwin

package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const ipTTLCmsgType = unix.IP_RECVTTL

// Darwin datagram ICMP sockets keep our identifier and deliver every ICMP
// message (including Time Exceeded) together with the IPv4 header.
func platformKernelAssignsID() bool { return false }

func platformSetup(c *sockConn, cfg *sockConfig) error {
	fd := c.fd
	if cfg.MarkSet {
		cfg.warn("-m (SO_MARK) is not supported on this platform, ignored")
	}
	if cfg.IfIndex > 0 {
		var err error
		if c.v6 {
			err = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, cfg.IfIndex)
		} else {
			err = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_BOUND_IF, cfg.IfIndex)
		}
		if err != nil {
			return fmt.Errorf("bind to interface %s: %v", cfg.IfName, err)
		}
	}
	switch cfg.PMTU {
	case "do", "dont":
		v := 0
		if cfg.PMTU == "do" {
			v = 1
		}
		var err error
		if c.v6 {
			err = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_DONTFRAG, v)
		} else {
			err = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_DONTFRAG, v)
		}
		if err != nil {
			cfg.warn("can't set DF: " + err.Error())
		}
	case "want", "probe":
		cfg.warn("-M " + cfg.PMTU + " is not supported on this platform, ignored")
	}
	return nil
}

func readErrQueue(c *sockConn) (Event, bool, error) { return Event{}, false, nil }

func localErrorText(e *Event) string { return "local error: " + unix.Errno(e.Errno).Error() }
