package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const version = "1.0.0"

// Options mirrors the iputils ping command line plus the advp extensions.
type Options struct {
	Host string

	Count       int           // -c, 0 = unlimited
	Interval    time.Duration // -i
	IntervalSet bool
	Linger      time.Duration // -W
	LingerSet   bool
	Deadline    time.Duration // -w, 0 = none
	Size        int           // -s
	TTL         int           // -t, -1 = system default
	Preload     int           // -l
	Interface   string        // -I
	Mark        int           // -m
	MarkSet     bool
	PMTU        string // -M do|dont|want|probe
	Pattern     []byte // -p
	TOS         int    // -Q, -1 = unset
	SndBuf      int    // -S
	Ident       int    // -e, -1 = auto
	Family      int    // 0, 4 or 6

	Precise     bool // -3
	Audible     bool // -a
	Adaptive    bool // -A
	Broadcast   bool // -b
	StickySrc   bool // -B
	Connect     bool // -C
	Debug       bool // -d
	Timestamps  bool // -D
	Flood       bool // -f
	ForceRDNS   bool // -H
	NoMcastLoop bool // -L
	Numeric     bool // -n
	Outstanding bool // -O
	Quiet       bool // -q
	DontRoute   bool // -r
	RecordRoute bool // -R
	UserLatency bool // -U
	Verbose     bool // -v

	// advp extensions
	Hops        int
	HopInterval time.Duration
	Bucket      time.Duration
	ChartHeight int
	TUI         bool
	NoReport    bool
	Color       string // auto, always, never
	CSV         string
	HTML        string

	Warnings []string
}

// errHelp / errVersion are returned when -h / -V are requested.
var (
	errHelp    = errors.New("help requested")
	errVersion = errors.New("version requested")
)

func defaultOptions() *Options {
	return &Options{
		Interval:    time.Second,
		Linger:      10 * time.Second,
		Size:        56,
		TTL:         -1,
		Preload:     1,
		TOS:         -1,
		Ident:       -1,
		Hops:        2,
		ChartHeight: 10,
		Color:       "auto",
	}
}

// Short options that take an argument, iputils style.
const shortWithArg = "cIilmMpQsStTwWeFN"
const shortFlags = "346aAbBCdDfhHLnOqrRUvV"

var longWithArg = map[string]bool{
	"hops": true, "hop-interval": true, "bucket": true, "csv": true,
	"html": true, "color": true, "chart-height": true,
}
var longFlags = map[string]bool{
	"help": true, "version": true, "tui": true, "dashboard": true,
	"no-hops": true, "no-report": true, "no-color": true,
}

// parseArgs parses argv (without the program name) the same way getopt does
// for iputils ping: bundled short flags (-qn), attached values (-c5), options
// after the destination, and "--" to stop option parsing.
func parseArgs(args []string) (*Options, error) {
	o := defaultOptions()
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			name, val, hasVal := strings.Cut(a[2:], "=")
			switch {
			case longFlags[name]:
				if hasVal {
					return nil, fmt.Errorf("option '--%s' doesn't allow an argument", name)
				}
				if err := o.applyLong(name, ""); err != nil {
					return nil, err
				}
			case longWithArg[name]:
				if !hasVal {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("option '--%s' requires an argument", name)
					}
					i++
					val = args[i]
				}
				if err := o.applyLong(name, val); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unrecognized option '%s'", a)
			}
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			switch {
			case strings.IndexByte(shortWithArg, c) >= 0:
				val := a[j+1:]
				if val == "" {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("option requires an argument -- '%c'", c)
					}
					i++
					val = args[i]
				}
				if err := o.applyShort(c, val); err != nil {
					return nil, err
				}
				j = len(a)
			case strings.IndexByte(shortFlags, c) >= 0:
				if err := o.applyShort(c, ""); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("invalid option -- '%c'", c)
			}
		}
	}

	switch len(positional) {
	case 0:
		return nil, errors.New("usage error: Destination address required")
	case 1:
		o.Host = positional[0]
	default:
		// iputils treats extra hosts as an IPv4 source route; unsupported here.
		o.Host = positional[len(positional)-1]
		o.Warnings = append(o.Warnings, "multiple destinations given; only the last one is used")
	}
	if o.Flood && !o.IntervalSet {
		o.Interval = 0
	}
	if o.Flood && o.Adaptive {
		return nil, errors.New("-f and -A are incompatible")
	}
	return o, nil
}

func (o *Options) applyShort(c byte, val string) error {
	var err error
	switch c {
	case '3':
		o.Precise = true
	case '4':
		if o.Family == 6 {
			return errors.New("only one -4 or -6 option may be specified")
		}
		o.Family = 4
	case '6':
		if o.Family == 4 {
			return errors.New("only one -4 or -6 option may be specified")
		}
		o.Family = 6
	case 'a':
		o.Audible = true
	case 'A':
		o.Adaptive = true
	case 'b':
		o.Broadcast = true
	case 'B':
		o.StickySrc = true
	case 'C':
		o.Connect = true
	case 'd':
		o.Debug = true
	case 'D':
		o.Timestamps = true
	case 'f':
		o.Flood = true
		o.Quiet = false
	case 'h':
		return errHelp
	case 'H':
		o.ForceRDNS = true
		o.Numeric = false
	case 'L':
		o.NoMcastLoop = true
	case 'n':
		o.Numeric = true
		o.ForceRDNS = false
	case 'O':
		o.Outstanding = true
	case 'q':
		o.Quiet = true
	case 'r':
		o.DontRoute = true
	case 'R':
		o.RecordRoute = true
	case 'U':
		o.UserLatency = true
	case 'v':
		o.Verbose = true
	case 'V':
		return errVersion
	case 'c':
		o.Count, err = parseIntRange(val, 1, math.MaxInt64)
	case 'i':
		o.Interval, err = parseSeconds(val, "bad timing interval")
		o.IntervalSet = true
	case 'I':
		o.Interface = val
	case 'l':
		o.Preload, err = parseIntRange(val, 1, 65536)
	case 'm':
		o.Mark, err = parseIntRange(val, 0, math.MaxUint32)
		o.MarkSet = true
	case 'M':
		switch val {
		case "do", "dont", "want", "probe":
			o.PMTU = val
		default:
			return fmt.Errorf("invalid -M argument: %s", val)
		}
	case 'p':
		o.Pattern, err = parsePattern(val)
	case 'Q':
		o.TOS, err = parseTOS(val)
	case 's':
		o.Size, err = parseIntRange(val, 0, math.MaxInt32)
	case 'S':
		o.SndBuf, err = parseIntRange(val, 1, math.MaxInt32)
	case 't':
		o.TTL, err = parseIntRange(val, 0, 255)
		if err != nil {
			err = errors.New("ttl out of range")
		}
	case 'T':
		o.Warnings = append(o.Warnings, "option -T (IP timestamp) is not supported, ignored")
	case 'w':
		o.Deadline, err = parseSeconds(val, "bad wait time")
	case 'W':
		o.Linger, err = parseSeconds(val, "bad linger time")
		if err == nil && o.Linger <= 0 {
			err = errors.New("bad linger time: " + val)
		}
		o.LingerSet = true
	case 'e':
		o.Ident, err = parseIntRange(val, 0, 65535)
	case 'F':
		o.Warnings = append(o.Warnings, "option -F (IPv6 flow label) is not supported, ignored")
	case 'N':
		o.Warnings = append(o.Warnings, "option -N (IPv6 node info) is not supported, ignored")
	}
	return err
}

func (o *Options) applyLong(name, val string) error {
	var err error
	switch name {
	case "help":
		return errHelp
	case "version":
		return errVersion
	case "tui", "dashboard":
		o.TUI = true
	case "no-hops":
		o.Hops = 0
	case "no-report":
		o.NoReport = true
	case "no-color":
		o.Color = "never"
	case "hops":
		o.Hops, err = parseIntRange(val, 0, 30)
	case "hop-interval":
		o.HopInterval, err = parseDurationArg(val)
		if err == nil && o.HopInterval < 100*time.Millisecond {
			err = errors.New("--hop-interval must be >= 100ms")
		}
	case "bucket":
		o.Bucket, err = parseDurationArg(val)
		if err == nil && o.Bucket <= 0 {
			err = errors.New("--bucket must be > 0")
		}
	case "chart-height":
		o.ChartHeight, err = parseIntRange(val, 3, 100)
	case "csv":
		o.CSV = val
	case "html":
		o.HTML = val
	case "color":
		switch val {
		case "auto", "always", "never":
			o.Color = val
		default:
			err = fmt.Errorf("invalid --color value %q (auto|always|never)", val)
		}
	}
	return err
}

func parseIntRange(s string, lo, hi int64) (int, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid argument: '%s'", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("invalid argument: '%s': out of range: %d <= value <= %d", s, lo, hi)
	}
	return int(v), nil
}

func parseSeconds(s, msg string) (time.Duration, error) {
	f, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f > float64(math.MaxInt32) {
		return 0, fmt.Errorf("%s: %s", msg, s)
	}
	return time.Duration(f * float64(time.Second)), nil
}

// parseDurationArg accepts Go durations ("30s", "1m30s") or plain seconds.
func parseDurationArg(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	return parseSeconds(s, "invalid duration")
}

func parsePattern(s string) ([]byte, error) {
	if s == "" || len(s) > 32 {
		return nil, errors.New("patterns must be specified as hex digits (max 16 bytes)")
	}
	if len(s)%2 == 1 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, errors.New("patterns must be specified as hex digits: " + s)
	}
	return b, nil
}

func parseTOS(s string) (int, error) {
	v, err := strconv.ParseInt(s, 0, 32)
	if err != nil || v < 0 || v > 255 {
		return 0, fmt.Errorf("the decimal value of TOS bits must be in range 0-255: %s", s)
	}
	return int(v), nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `
Usage
  advp [options] <destination>

Options:
  <destination>      DNS name or IP address
  -3                 RTT precision (do not round up the result time)
  -a                 use audible ping
  -A                 use adaptive ping
  -B                 sticky source address
  -c <count>         stop after <count> replies
  -C                 call connect() syscall on socket creation
  -D                 print timestamps
  -d                 use SO_DEBUG socket option
  -e <identifier>    define identifier for ping session, default is random for
                     SOCK_RAW and kernel defined for SOCK_DGRAM
  -f                 flood ping
  -h                 print help and exit
  -H                 force reverse DNS name resolution (useful for numeric
                     destinations or for -f), override -n
  -I <interface>     either interface name or address
  -i <interval>      seconds between sending each packet
  -L                 suppress loopback of multicast packets
  -l <preload>       send <preload> number of packages while waiting replies
  -m <mark>          tag the packets going out (Linux)
  -M <pmtud opt>     define path MTU discovery, can be one of <do|dont|want|probe>
  -n                 no reverse DNS name resolution, override -H
  -O                 report outstanding replies
  -p <pattern>       contents of padding byte
  -q                 quiet output
  -Q <tclass>        use quality of service <tclass> bits
  -s <size>          use <size> as number of data bytes to be sent
  -S <size>          use <size> as SO_SNDBUF socket option value
  -t <ttl>           define time to live
  -U                 print user-to-user latency
  -v                 verbose output
  -V                 print version and exit
  -w <deadline>      reply wait <deadline> in seconds
  -W <timeout>       time to wait for response

IPv4 options:
  -4                 use IPv4
  -b                 allow pinging broadcast
  -R                 record route (raw socket only)
  -T <timestamp>     (accepted, not supported)

IPv6 options:
  -6                 use IPv6
  -F <flowlabel>     (accepted, not supported)
  -N <nodeinfo opt>  (accepted, not supported)

advp extensions:
  --tui, --dashboard     live full-screen dashboard instead of per-packet lines
  --hops <n>             number of first hops to monitor (default 2, 0 disables)
  --no-hops              do not probe the first hops
  --hop-interval <dur>   interval between hop probes (default max(1s, -i))
  --bucket <dur>         time-bucket width for the timeline table (default auto)
  --chart-height <n>     height of the RTT chart in rows (default 10)
  --csv <file>           write every probe (target and hops) to a CSV file
  --html <file>          write a self-contained HTML report with SVG charts
  --no-report            print only the classic ping summary
  --color <when>         auto|always|never (default auto, honours NO_COLOR)
  --no-color             same as --color never
`)
}
