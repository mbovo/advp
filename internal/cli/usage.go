package cli

import (
	"fmt"
	"io"
)

// printUsage prints the -h help text.
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
  --live                 full-screen live report refreshed on every packet sent
                         and received (keys: q quit, p pause, v view, r redraw);
                         --tui and --dashboard are aliases
  --hops <n>             number of first hops to monitor (default 2, 0 disables)
  --no-hops              do not probe the first hops
  --hops-max[=N]         trace the route first (TTL 1..N, default 30, 3 probes
                         per hop) and then monitor every hop up to the
                         destination; overrides --hops
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
