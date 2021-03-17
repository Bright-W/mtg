package network

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/9seconds/mtg/v2/mtglib"
	doh "github.com/babolivier/go-doh-client"
)

type networkHTTPTransport struct {
	userAgent string
	next      http.RoundTripper
}

func (n networkHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", n.userAgent)

	return n.next.RoundTrip(req)
}

type network struct {
	dialer      Dialer
	dns         doh.Resolver
	httpTimeout time.Duration
	userAgent   string
}

func (n *network) Dial(protocol, address string) (net.Conn, error) {
	return n.DialContext(context.Background(), protocol, address)
}

func (n *network) DialContext(ctx context.Context, protocol, address string) (net.Conn, error) {
	host, port, _ := net.SplitHostPort(address)

	ips, err := n.dnsResolve(protocol, host)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve dns names: %w", err)
	}

	if len(ips) > 1 {
		rand.Shuffle(len(ips), func(i, j int) {
			ips[i], ips[j] = ips[j], ips[i]
		})
	}

	var conn net.Conn
	for _, v := range ips {
		conn, err = n.dialer.DialContext(ctx, protocol, net.JoinHostPort(v, port))

		if err == nil {
			return conn, nil
		}
	}

	return nil, fmt.Errorf("cannot dial to %s:%s: %w", protocol, address, err)
}

func (n *network) MakeHTTPClient(dialFunc func(ctx context.Context,
	network, address string) (net.Conn, error)) *http.Client {
	if dialFunc == nil {
		dialFunc = n.DialContext
	}

	return makeHTTPClient(n.userAgent, n.httpTimeout, dialFunc)
}

func (n *network) dnsResolve(protocol, address string) ([]string, error) {
	if net.ParseIP(address) != nil {
		return []string{address}, nil
	}

	ips := []string{}
	wg := &sync.WaitGroup{}
	mutex := &sync.Mutex{}

	switch protocol {
	case "tcp", "tcp4":
		wg.Add(1)

		go func() {
			defer wg.Done()

			if recs, _, err := n.dns.LookupA(address); err == nil {
				mutex.Lock()
				defer mutex.Unlock()

				for _, v := range recs {
					ips = append(ips, v.IP4)
				}
			}
		}()
	}

	switch protocol {
	case "tcp", "tcp6":
		wg.Add(1)

		go func() {
			defer wg.Done()

			if recs, _, err := n.dns.LookupAAAA(address); err == nil {
				mutex.Lock()
				defer mutex.Unlock()

				for _, v := range recs {
					ips = append(ips, v.IP6)
				}
			}
		}()
	}

	wg.Wait()

	if len(ips) == 0 {
		return nil, fmt.Errorf("cannot find any ips for %s:%s", protocol, address)
	}

	return ips, nil
}

func NewNetwork(dialer Dialer,
	userAgent, dohHostname string,
	httpTimeout time.Duration) (mtglib.Network, error) {
	switch {
	case httpTimeout < 0:
		return nil, fmt.Errorf("timeout should be positive number %s", httpTimeout)
	case httpTimeout == 0:
		httpTimeout = DefaultHTTPTimeout
	}

	if net.ParseIP(dohHostname) == nil {
		return nil, fmt.Errorf("hostname %s should be IP address", dohHostname)
	}

	return &network{
		dialer:      dialer,
		httpTimeout: httpTimeout,
		userAgent:   userAgent,
		dns: doh.Resolver{
			Host:       dohHostname,
			Class:      doh.IN,
			HTTPClient: makeHTTPClient(userAgent, DNSTimeout, dialer.DialContext),
		},
	}, nil
}

func makeHTTPClient(userAgent string,
	timeout time.Duration,
	dialFunc func(ctx context.Context, network, address string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: networkHTTPTransport{
			userAgent: userAgent,
			next: &http.Transport{
				DialContext: dialFunc,
			},
		},
	}
}
