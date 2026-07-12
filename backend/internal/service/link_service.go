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
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
					return nil, fmt.Errorf("refusing to fetch private address %s", ip)
				}
			}
			return dialer.DialContext(ctx, network, addr)
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
