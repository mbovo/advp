package ping

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

type nameCache struct {
	mu sync.Mutex
	m  map[string]*nameEntry
}

type nameEntry struct {
	done chan struct{}
	name string
}

func newNameCache() *nameCache { return &nameCache{m: map[string]*nameEntry{}} }

// Lookup returns the reverse DNS name of ip, waiting at most wait for an
// in-flight lookup; results are cached.
func (c *nameCache) Lookup(ip string, wait time.Duration) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	e, ok := c.m[ip]
	if !ok {
		e = &nameEntry{done: make(chan struct{})}
		c.m[ip] = e
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil && len(names) > 0 {
				e.name = strings.TrimSuffix(names[0], ".")
			}
			close(e.done)
		}()
	}
	c.mu.Unlock()
	if wait <= 0 {
		select {
		case <-e.done:
			return e.name
		default:
			return ""
		}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-e.done:
		return e.name
	case <-t.C:
		return ""
	}
}
