package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/jessevdk/go-flags"
	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
)

// Flags
var opts struct {
	Name      string   `short:"q" long:"qname" description:"Query name"`
	Server    string   `short:"s" long:"server" description:"DNS server"`
	Types     []string `short:"t" long:"type" description:"RR type"`
	Reverse   bool     `short:"x" long:"reverse" description:"Reverse lookup"`
	DNSSEC    bool     `short:"d" long:"dnssec" description:"Request DNSSEC"`
	Raw       bool     `short:"r" long:"raw" description:"Output raw DNS format"`
	Chaos     bool     `short:"c" long:"chaos" description:"Use CHAOS query class"`
	OdohProxy string   `short:"p" long:"odoh-proxy" description:"ODoH proxy"`
	Insecure  bool     `short:"i" long:"insecure" description:"Disable TLS certificate verification"`
	Verbose   bool     `short:"v" long:"verbose" description:"Show verbose log messages"`
}

var version = "dev" // Set by build process

// ANSI colors
var colors = map[string]string{
	"black":   "\033[1;30m%s\033[0m",
	"red":     "\033[1;31m%s\033[0m",
	"green":   "\033[1;32m%s\033[0m",
	"yellow":  "\033[1;33m%s\033[0m",
	"purple":  "\033[1;34m%s\033[0m",
	"magenta": "\033[1;35m%s\033[0m",
	"teal":    "\033[1;36m%s\033[0m",
	"white":   "\033[1;37m%s\033[0m",
}

// color returns a color formatted string
func color(color string, args ...interface{}) string {
	return fmt.Sprintf(colors[color], fmt.Sprint(args...))
}

func main() {
	// Parse cli flags
	_, err := flags.ParseArgs(&opts, os.Args)
	if err != nil {
		if !strings.Contains(err.Error(), "Usage") {
			log.Fatal(err)
		}
		os.Exit(1)
	}

	// Enable debug logging in development releases
	if //noinspection GoBoolExpressions
	version == "devel" || opts.Verbose {
		log.SetLevel(log.DebugLevel)
	}

	// Find a server by @ symbol if it isn't set by flag
	if opts.Server == "" {
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "@") {
				opts.Server = strings.TrimPrefix(arg, "@")
			}
		}
	}

	// Parse requested RR types
	var rrTypes []uint16
	for _, rrType := range opts.Types {
		typeCode, ok := dns.StringToType[strings.ToUpper(rrType)]
		if ok {
			rrTypes = append(rrTypes, typeCode)
		} else {
			fmt.Printf("%s is not a valid RR type\n", rrType)
			os.Exit(1)
		}
	}

	// Add non-flag RR types
	for _, arg := range os.Args {
		rrType, ok := dns.StringToType[strings.ToUpper(arg)]
		if ok {
			rrTypes = append(rrTypes, rrType)
		}
	}

	// If no RR types are defined, set a list of default ones
	if len(rrTypes) < 1 {
		for _, defaultRRType := range []string{"A", "AAAA", "NS", "MX", "TXT", "CNAME"} {
			rrType, _ := dns.StringToType[defaultRRType]
			rrTypes = append(rrTypes, rrType)
		}
	}

	if opts.Verbose {
		var rrTypeStrings []string
		for _, rrType := range rrTypes {
			rrTypeStrings = append(rrTypeStrings, dns.TypeToString[rrType])
		}
		log.Debugf("RR types: %+v", rrTypeStrings)
	}

	// Set qname if not set by flag
	for _, arg := range os.Args {
		if strings.Contains(arg, ".") && !strings.Contains(arg, "@") {
			opts.Name = arg
		}
	}

	// Reverse address if required
	if opts.Reverse {
		opts.Name, err = dns.ReverseAddr(opts.Name)
		if err != nil {
			log.Fatal(err)
		}
		rrTypes = append(rrTypes, dns.StringToType["PTR"])
	}

	log.Debugf("qname %s", opts.Name)

	// Create the upstream server
	u, err := upstream.AddressToUpstream(opts.Server, upstream.Options{
		Timeout:            10 * time.Second,
		InsecureSkipVerify: opts.Insecure,
	})
	if err != nil {
		log.Fatalf("cannot create upstream %v", err)
	}

	log.Debugf("using server %s\n", u.Address())

	var replies []*dns.Msg
	queryStartTime := time.Now()

	// Query for each requested RR type
	for _, qType := range rrTypes {
		req := dns.Msg{}

		// Create the DNS question
		if opts.DNSSEC {
			req.SetEdns0(4096, true)
		}

		// Set QCLASS
		var class uint16
		if opts.Chaos {
			class = dns.ClassCHAOS
		} else {
			class = dns.ClassINET
		}
		req.RecursionDesired = true
		req.Question = []dns.Question{{
			Name:   dns.Fqdn(opts.Name),
			Qtype:  qType,
			Qclass: class,
		}}

		var reply *dns.Msg
		// Use upstream exchange if no ODoH proxy is configured
		if opts.OdohProxy == "" {
			// Send question to server
			reply, err = u.Exchange(&req)
		} else {
			log.Debugf("using ODoH proxy %s", opts.OdohProxy)
			reply, err = odohQuery(req, opts.OdohProxy, opts.Server)
		}
		if err != nil {
			log.Fatalf("upstream query: %s", err)
		}

		replies = append(replies, reply)

		// Print answers
		for _, answer := range reply.Answer {
			if opts.Raw {
				fmt.Println(answer.String())
			} else {
				hdr := answer.Header()
				fmt.Printf("%s %s %s %s\n",
					color("purple", hdr.Name),
					color("green", time.Duration(hdr.Ttl)*time.Second),
					color("magenta", dns.TypeToString[hdr.Rrtype]),
					strings.TrimSpace(strings.Join(strings.Split(answer.String(), dns.TypeToString[hdr.Rrtype])[1:], "")),
				)
			}
		}

	}

	queryTime := time.Now().Sub(queryStartTime)
	if opts.Raw {
		fmt.Printf(";; Received %d replies from %s in %s\n", len(replies), u.Address(), queryTime.Round(time.Millisecond))
	}
}
