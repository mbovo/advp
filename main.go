package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opt, err := parseArgs(args)
	switch {
	case errors.Is(err, errHelp):
		printUsage(stdout)
		return 0
	case errors.Is(err, errVersion):
		fmt.Fprintf(stdout, "advp %s (iputils ping compatible)\n", version)
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "advp: %v\nTry 'advp -h' for more information.\n", err)
		return 2
	}

	root := os.Geteuid() == 0
	if !root && (opt.IntervalSet || opt.Flood) && opt.Interval < 2*time.Millisecond {
		fmt.Fprintln(stderr, "advp: cannot flood, minimal interval for user must be >= 2 ms, use -i 0.002 (or higher)")
		return 2
	}
	if !root && opt.Preload > 3 {
		fmt.Fprintf(stderr, "advp: cannot set preload to value greater than 3: %d\n", opt.Preload)
		return 2
	}

	dst, zone, isName, err := resolve(opt.Host, opt.Family)
	if err != nil {
		fmt.Fprintf(stderr, "advp: %s: %v\n", opt.Host, err)
		return 2
	}
	v6 := dst.To4() == nil
	if maxSize := map[bool]int{false: 65507, true: 65527}[v6]; opt.Size > maxSize {
		fmt.Fprintf(stderr, "advp: invalid -s value: '%d': out of range: 0 <= value <= %d\n", opt.Size, maxSize)
		return 2
	}
	if v6 && opt.RecordRoute {
		opt.Warnings = append(opt.Warnings, "-R is an IPv4 option, ignored")
		opt.RecordRoute = false
	}
	if !v6 && isBroadcast(dst) && !opt.Broadcast {
		fmt.Fprintln(stderr, "advp: Do you want to ping broadcast? Then -b. If not, check your local firewall rules")
		return 2
	}

	warnings := opt.Warnings
	cfg := sockConfig{
		V6: v6, Dst: dst, Zone: zone, TTL: opt.TTL, TOS: opt.TOS, Mark: opt.Mark, MarkSet: opt.MarkSet,
		PMTU: opt.PMTU, Broadcast: opt.Broadcast, DontRoute: opt.DontRoute, Debug: opt.Debug,
		NoMcastLoop: opt.NoMcastLoop, Connect: opt.Connect, RecordRoute: opt.RecordRoute,
		PreferRaw: opt.RecordRoute, SndBuf: opt.SndBuf, Ident: opt.Ident, Warnings: &warnings,
	}
	if opt.Interface != "" {
		if ip := net.ParseIP(opt.Interface); ip != nil {
			cfg.Src = ip
		} else {
			ifi, err := net.InterfaceByName(opt.Interface)
			if err != nil {
				fmt.Fprintf(stderr, "advp: unknown iface: %s\n", opt.Interface)
				return 2
			}
			cfg.IfName, cfg.IfIndex = ifi.Name, ifi.Index
			if zone == "" && v6 && dst.IsLinkLocalUnicast() {
				cfg.Zone = ifi.Name
			}
		}
	}

	conn, err := openICMP(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "advp: %v\n", err)
		return 2
	}
	if opt.RecordRoute && !conn.Raw() {
		warnings = append(warnings, "-R needs a raw socket (root / CAP_NET_RAW) to read the route back")
	}

	var hops []*hopState
	for ttl := 1; ttl <= opt.Hops; ttl++ {
		hc := cfg
		hc.TTL, hc.Ident, hc.RecordRoute, hc.PreferRaw = ttl, -1, false, false
		var hw []string
		hc.Warnings = &hw
		c, err := openICMP(hc)
		if err == nil && c.FilterID() && c.ID() == conn.ID() {
			c.Close()
			c, err = openICMP(hc)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("hop probing disabled: %v", err))
			for _, h := range hops {
				h.conn.Close()
			}
			hops = nil
			break
		}
		hops = append(hops, &hopState{ttl: ttl, conn: c, series: newSeries()})
	}

	for _, w := range warnings {
		fmt.Fprintf(stderr, "advp: warning: %s\n", w)
	}

	stdoutTTY := isTerminal(stdout)
	color := opt.Color == "always" || (opt.Color == "auto" && stdoutTTY && os.Getenv("NO_COLOR") == "")
	kind := "datagram (unprivileged)"
	if conn.Raw() {
		kind = "raw"
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	host := opt.Host
	if isName {
		host = strings.TrimSuffix(host, ".")
	}
	p := &Pinger{
		opt: opt, out: stdout, errOut: stderr, host: host, dst: dst, v6: v6, isName: isName,
		conn: conn, hops: hops, names: newNameCache(), sigs: sigs, color: color, root: root, sockKind: kind,
	}
	if opt.TUI {
		if !stdoutTTY {
			fmt.Fprintln(stderr, "advp: warning: --tui needs a terminal, falling back to line output")
		} else {
			p.dash = &dashboard{w: stdout, st: style{on: color}}
		}
	}
	return p.Run()
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func resolve(host string, family int) (ip net.IP, zone string, isName bool, err error) {
	h, z, _ := strings.Cut(host, "%")
	if ip := net.ParseIP(h); ip != nil {
		if family == 4 && ip.To4() == nil || family == 6 && ip.To4() != nil {
			return nil, "", false, errors.New("Address family for hostname not supported")
		}
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		return ip, z, false, nil
	}
	network := "ip"
	switch family {
	case 4:
		network = "ip4"
	case 6:
		network = "ip6"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil || len(ips) == 0 {
		return nil, "", false, errors.New("Name or service not known")
	}
	ip = ips[0]
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip, "", true, nil
}

func isBroadcast(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4.Equal(net.IPv4bcast) {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() == nil || len(n.Mask) != net.IPv4len {
			continue
		}
		ones, _ := n.Mask.Size()
		if ones >= 31 {
			continue
		}
		b := make(net.IP, 4)
		for i := range b {
			b[i] = n.IP.To4()[i] | ^n.Mask[i]
		}
		if b.Equal(ip4) {
			return true
		}
	}
	return false
}
