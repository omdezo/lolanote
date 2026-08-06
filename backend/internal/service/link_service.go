package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"qomranote/backend/internal/domain"
)

// LinkService fetches page metadata server-side for Link cards (§4.4):
// title, description, thumbnail, canonical URL, and rich-embed detection.
type LinkService struct {
	client *http.Client
}

// NewLinkService builds an HTTP client with SSRF protection: only public
// addresses are dialed.
func NewLinkService() *LinkService {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			// DIAL THE IP WE CHECKED, never the hostname.
			//
			// Resolving, validating, and then handing the hostname back to the
			// dialer re-resolves it — so a record with a short TTL can answer
			// with a public address for the check and 169.254.169.254 or
			// 127.0.0.1 for the connection. The check and the connection have
			// to be about the same address or the check means nothing.
			//
			// This matters more than it did: the agent can now reach this path
			// through read_url, so the URL is no longer only ever one a person
			// pasted deliberately.
			var lastErr error
			for _, ip := range ips {
				if !publicIP(ip) {
					lastErr = fmt.Errorf("refusing to fetch private address %s", ip)
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no usable address for %s", host)
			}
			return nil, lastErr
		},
	}
	return &LinkService{client: &http.Client{Timeout: 10 * time.Second, Transport: transport}}
}

// LinkMetadata is the resolved preview for a Link card.
type LinkMetadata struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnailUrl"`
	SiteName     string `json:"siteName"`
	EmbedType    string `json:"embedType"` // youtube | vimeo | spotify | soundcloud | googlemaps | ""
}

// embedHosts maps recognized rich-embed sources (§4.4/§4.5–4.7).
var embedHosts = map[string]string{
	"youtube.com": "youtube", "youtu.be": "youtube", "www.youtube.com": "youtube",
	"vimeo.com": "vimeo", "www.vimeo.com": "vimeo",
	"open.spotify.com": "spotify",
	"soundcloud.com":   "soundcloud", "www.soundcloud.com": "soundcloud",
	"maps.google.com": "googlemaps", "www.google.com": "", // /maps handled below
	"maps.app.goo.gl": "googlemaps",
	"codepen.io":      "codepen", "dribbble.com": "dribbble", "www.dribbble.com": "dribbble",
}

// Resolve fetches and parses metadata for a URL.
func (s *LinkService) Resolve(ctx context.Context, rawURL string) (*LinkMetadata, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, domain.ErrValidation
	}

	meta := &LinkMetadata{URL: parsed.String(), EmbedType: detectEmbed(parsed)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "QomraNoteBot/1.0 (+link preview)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := s.client.Do(req)
	if err != nil {
		// Unreachable pages still make a usable link card.
		meta.Title = parsed.Host
		return meta, nil
	}
	defer resp.Body.Close()

	doc, err := html.Parse(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		meta.Title = parsed.Host
		return meta, nil
	}
	extractMeta(doc, meta)
	if meta.Title == "" {
		meta.Title = parsed.Host
	}
	if meta.ThumbnailURL != "" {
		if thumb, err := url.Parse(meta.ThumbnailURL); err == nil {
			meta.ThumbnailURL = parsed.ResolveReference(thumb).String()
		}
	}
	return meta, nil
}

func detectEmbed(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if kind, ok := embedHosts[host]; ok && kind != "" {
		return kind
	}
	if strings.Contains(host, "google.") && strings.HasPrefix(u.Path, "/maps") {
		return "googlemaps"
	}
	return ""
}

// extractMeta walks the HTML tree collecting <title> and OpenGraph tags.
func extractMeta(n *html.Node, meta *LinkMetadata) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "title":
			if meta.Title == "" && n.FirstChild != nil {
				meta.Title = strings.TrimSpace(n.FirstChild.Data)
			}
		case "meta":
			var property, name, content string
			for _, attr := range n.Attr {
				switch attr.Key {
				case "property":
					property = attr.Val
				case "name":
					name = attr.Val
				case "content":
					content = attr.Val
				}
			}
			switch {
			case property == "og:title" && content != "":
				meta.Title = content
			case (property == "og:description" || name == "description") && meta.Description == "":
				meta.Description = content
			case property == "og:image" && meta.ThumbnailURL == "":
				meta.ThumbnailURL = content
			case property == "og:site_name":
				meta.SiteName = content
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractMeta(c, meta)
	}
}

// reservedNets are the blocks net.IP's own predicates do not cover.
//
// IsPrivate is RFC1918 and fc00::/7 and nothing else, and the gap is not
// academic — 100.64.0.0/10 is where cloud providers put the things an instance
// is not supposed to reach from the outside: managed metadata proxies,
// control-plane agents, a Kubernetes node's own service range. It is neither
// public nor RFC1918, so it read here as "the ordinary internet" and would have
// been dialed. 198.18.0.0/15 and 192.0.0.0/24 are the same argument with less at
// stake: nothing legitimate is served from them, and some networks route them.
var reservedNets = []*net.IPNet{
	mustCIDR("100.64.0.0/10"),   // RFC 6598 shared address space (CGNAT, cloud internals)
	mustCIDR("192.0.0.0/24"),    // IETF protocol assignments
	mustCIDR("198.18.0.0/15"),   // benchmarking
	mustCIDR("192.0.2.0/24"),    // TEST-NET-1
	mustCIDR("198.51.100.0/24"), // TEST-NET-2
	mustCIDR("203.0.113.0/24"),  // TEST-NET-3
	mustCIDR("240.0.0.0/4"),     // reserved for future use
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("link service: bad reserved CIDR " + s)
	}
	return n
}

// embeddedV4 extracts the IPv4 address hiding inside an IPv6 one, for the three
// encodings that carry one.
//
// The file already knew about ::ffff:0:0/96 — a v4-mapped address is a private
// v4 address wearing a v6 shape, and the v4 predicates cannot see through it.
// It is not the only such shape. 64:ff9b::/96 is the well-known NAT64 prefix, so
// on any host with NAT64 configured `64:ff9b::7f00:1` IS 127.0.0.1; 2002::/16 is
// 6to4, where the address after the prefix is the v4 relay endpoint. Both reach
// a v4 destination through a name that resolves to something the v4 checks never
// examine, which is exactly the bypass the v4-mapped case was fixed for.
func embeddedV4(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4 // includes the v4-mapped form, which To4 already unwraps
	}
	v6 := ip.To16()
	if v6 == nil {
		return nil
	}
	// 64:ff9b::/96 — NAT64. The last four bytes are the v4 address.
	if v6[0] == 0x00 && v6[1] == 0x64 && v6[2] == 0xff && v6[3] == 0x9b &&
		v6[4] == 0 && v6[5] == 0 && v6[6] == 0 && v6[7] == 0 &&
		v6[8] == 0 && v6[9] == 0 && v6[10] == 0 && v6[11] == 0 {
		return net.IPv4(v6[12], v6[13], v6[14], v6[15]).To4()
	}
	// 2002::/16 — 6to4. Bytes 2..5 are the v4 relay endpoint.
	if v6[0] == 0x20 && v6[1] == 0x02 {
		return net.IPv4(v6[2], v6[3], v6[4], v6[5]).To4()
	}
	return nil
}

// publicIP reports whether an address is one this server will reach out to.
//
// Deny-listed rather than allow-listed because the space of things that must
// not be reachable is the one that is enumerable: loopback, RFC1918, link-local
// (which is where cloud metadata endpoints live), multicast, the unspecified
// address, and the reserved blocks the stdlib has no predicate for. Anything
// else is the public internet.
func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// An IPv6 address that CARRIES a v4 one is judged as that v4 address. Doing
	// it before the block list matters: the blocks below are written in v4, and
	// a v4 destination smuggled inside a v6 encoding would otherwise miss all of
	// them the same way it used to miss IsPrivate.
	if v4 := embeddedV4(ip); v4 != nil && !ip.Equal(v4) {
		return publicIP(v4)
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
