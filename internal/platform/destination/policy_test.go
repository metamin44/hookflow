package destination

import (
	"errors"
	"net/netip"
	"testing"
)

func TestProhibitedIPv4Ranges(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			addr := netip.MustParsePrefix(value).Addr()
			if err := CheckAddr(addr); !errors.Is(err, ErrProhibited) {
				t.Fatalf("CheckAddr(%s) = %v, want prohibited", addr, err)
			}
		})
	}
}

func TestProhibitedIPv6Ranges(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"::/128", "::1/128", "::/96", "::ffff:0:0/96", "64:ff9b::/96",
		"64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
		"3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			addr := netip.MustParsePrefix(value).Addr()
			if err := CheckAddr(addr); !errors.Is(err, ErrProhibited) {
				t.Fatalf("CheckAddr(%s) = %v, want prohibited", addr, err)
			}
		})
	}
}

func TestClassifiedAddressesAreProhibited(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"169.254.10.20", "224.0.0.1", "255.255.255.255", "0.0.0.0",
		"fe80::1", "ff02::1", "ff01::1", "fd00::1", "::1", "::",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if err := CheckAddr(netip.MustParseAddr(value)); !errors.Is(err, ErrProhibited) {
				t.Fatalf("CheckAddr(%s) = %v, want prohibited", value, err)
			}
		})
	}
}

func TestMappedIPv6IsUnmappedBeforeClassification(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:192.168.1.1", "::ffff:8.8.4.4"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			err := CheckAddr(netip.MustParseAddr(value))
			if value == "::ffff:8.8.4.4" {
				if err != nil {
					t.Fatalf("CheckAddr(%s) = %v, want allowed", value, err)
				}
				return
			}
			if !errors.Is(err, ErrProhibited) {
				t.Fatalf("CheckAddr(%s) = %v, want prohibited", value, err)
			}
		})
	}
}

func TestZoneIdentifiersAreProhibited(t *testing.T) {
	t.Parallel()
	if err := CheckAddr(netip.MustParseAddr("2606:4700:4700::1111%eth0")); !errors.Is(err, ErrProhibited) {
		t.Fatalf("zoned public address = %v, want prohibited", err)
	}
}

func TestPublicAddressesAreAllowed(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111",
		"2001:4860:4860::8888", "::ffff:1.1.1.1",
	} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if err := CheckAddr(netip.MustParseAddr(value)); err != nil {
				t.Fatalf("CheckAddr(%s) = %v, want allowed", value, err)
			}
		})
	}
}

func TestParseURLRestrictions(t *testing.T) {
	t.Parallel()
	valid := []string{
		"https://example.test/hook",
		"https://example.test:443/hook",
		"https://1.1.1.1/hook",
		"https://[2606:4700:4700::1111]/hook",
	}
	for _, value := range valid {
		if _, err := ParseURL(value); err != nil {
			t.Fatalf("ParseURL(%q) = %v, want nil", value, err)
		}
	}

	invalid := map[string]string{
		"http":            "http://example.test/hook",
		"relative":        "/hook",
		"no host":         "https:///hook",
		"opaque":          "https:example.test",
		"userinfo":        "https://user:pass@example.test/hook",
		"fragment":        "https://example.test/hook#frag",
		"empty fragment":  "https://example.test/hook#",
		"port 8443":       "https://example.test:8443/hook",
		"port 80":         "https://example.test:80/hook",
		"empty port":      "https://example.test:/hook",
		"ipv6 zone":       "https://[fe80::1%25eth0]/hook",
		"private literal": "https://10.0.0.5/hook",
		"loopback":        "https://127.0.0.1/hook",
		"ipv6 loopback":   "https://[::1]/hook",
		"metadata":        "https://169.254.169.254/latest/meta-data",
		"leading space":   " https://example.test/hook",
		"trailing space":  "https://example.test/hook ",
		"inner space":     "https://example.test/a b",
		"tab":             "https://example.test/\thook",
		"newline":         "https://example.test/hook\n",
		"carriage return": "https://example.test/hook\r",
		"nul":             "https://example.test/hook\x00",
		"del":             "https://example.test/hook\x7f",
		"unit separator":  "https://example.test/hook\x1f",
	}
	for name, value := range invalid {
		name, value := name, value
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseURL(value); !errors.Is(err, ErrProhibited) {
				t.Fatalf("ParseURL(%q) = %v, want prohibited", value, err)
			}
		})
	}
}
