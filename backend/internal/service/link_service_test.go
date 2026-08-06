package service

import (
	"context"
	"errors"
	"net"
	"testing"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The SSRF guard is only as good as its address classification. Cloud metadata
// lives on a link-local address, and an IPv4-mapped IPv6 form hides a private
// v4 address behind a v6 shape that the stdlib predicates do not see through.
func TestPublicIP_RefusesEverythingInternal(t *testing.T) {
	refuse := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.5", "172.16.4.1", "192.168.1.10", // RFC1918
		"169.254.169.254", // cloud metadata — the one that matters most
		"fe80::1",         // link-local v6
		"0.0.0.0", "::",   // unspecified
		"224.0.0.1", "ff02::1", // multicast
		"::ffff:127.0.0.1", // v4-mapped loopback
		"::ffff:10.1.2.3",  // v4-mapped private
	}
	for _, s := range refuse {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test fixture %q is not an IP", s)
		}
		if publicIP(ip) {
			t.Errorf("%s was treated as public; it must be refused", s)
		}
	}

	allow := []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700::1111"}
	for _, s := range allow {
		if !publicIP(net.ParseIP(s)) {
			t.Errorf("%s was refused; ordinary public addresses must be reachable", s)
		}
	}
}

// SEC1. The classifier trusted net.IP's own predicates to mean "internal", and
// they do not: IsPrivate is RFC1918 plus fc00::/7 and nothing else. Two families
// of address walked straight through it.
//
// 100.64.0.0/10 is RFC 6598 shared space, which is where a cloud provider puts
// the endpoints an instance is not meant to reach from outside — a managed
// metadata proxy, a control-plane agent, a node's own service range. It is
// neither public nor RFC1918, so it read as "the ordinary internet".
//
// And the v4-mapped fix stopped one encoding short. 64:ff9b::/96 (NAT64) and
// 2002::/16 (6to4) also carry a v4 destination inside a v6 address, so
// `64:ff9b::7f00:1` is 127.0.0.1 on any host with NAT64 configured — the exact
// bypass ::ffff:127.0.0.1 was closed for, one encoding along.
//
// This matters because the agent reaches this path through read_url, so the URL
// is no longer only ever one a person pasted deliberately.
func TestPublicIP_RefusesReservedSpaceAndV4SmuggledInsideV6(t *testing.T) {
	refuse := map[string]string{
		"100.64.0.1":         "RFC 6598 shared space — cloud provider internals live here",
		"100.127.255.254":    "the top of RFC 6598",
		"192.0.0.170":        "IETF protocol assignments",
		"198.18.0.1":         "benchmarking range",
		"240.0.0.1":          "reserved for future use",
		"64:ff9b::7f00:1":    "NAT64-encoded 127.0.0.1",
		"64:ff9b::a9fe:a9fe": "NAT64-encoded 169.254.169.254, the cloud metadata address",
		"2002:7f00:1::":      "6to4-encoded 127.0.0.1",
		"2002:a9fe:a9fe::":   "6to4-encoded 169.254.169.254",
		"2002:c0a8:101::":    "6to4-encoded 192.168.1.1",
	}
	for addr, why := range refuse {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("test fixture %q is not an IP", addr)
		}
		if publicIP(ip) {
			t.Errorf("%s was treated as public (%s)", addr, why)
		}
	}

	// And a 6to4 address whose embedded v4 IS public stays reachable, so the
	// rule is "judge the destination", not "refuse the prefix".
	if !publicIP(net.ParseIP("2002:0808:0808::")) { // 8.8.8.8 in 6to4
		t.Error("a 6to4 address wrapping a public v4 destination was refused")
	}
}

// Labels are private to their owner, so attaching one you do not own both
// reveals that it exists and inflates its usage count from outside the account.
// The agent's path always checked this; the path a person uses did not.
func TestLabelAttach_RefusesSomeoneElsesLabel(t *testing.T) {
	_, elements, items := fixture(t)
	ctx := context.Background()
	labels := memory.NewLabelRepo()
	svc := NewLabelService(labels, elements, NewAccessResolver(elements), IDGenerator(func() string { return "lbl00000000000000000001" }))

	// Bob's label, on Bob's account.
	if err := labels.Insert(ctx, &domain.Label{ID: "lbl00000000000000000009", OwnerID: "bob", Name: "Private"}); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	alice := &domain.Principal{Sub: "alice"}
	err := svc.Attach(ctx, alice, items["cardA"].ID, "lbl00000000000000000009")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("attaching another owner's label returned %v; want forbidden", err)
	}

	// And her own label still works, so the guard is not simply blocking.
	if err := labels.Insert(ctx, &domain.Label{ID: "lbl00000000000000000010", OwnerID: "alice", Name: "Mine"}); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	if err := svc.Attach(ctx, alice, items["cardA"].ID, "lbl00000000000000000010"); err != nil {
		t.Fatalf("attaching own label failed: %v", err)
	}
}
