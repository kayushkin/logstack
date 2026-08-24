package client

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kayushkin/logstack/models"
)

// pathProbeValues is the table every caller below is driven with. Each entry
// carries a character that changes which endpoint the request addresses if it
// reaches the path unescaped.
//
// The space probe is here to separate PathEscape from QueryEscape (%20 against
// +), not to witness the defect: net/url escapes a raw space to %20 on its own,
// so an unescaped space round-trips correctly and cannot fail for the reason its
// neighbours fail.
var pathProbeValues = []struct {
	name  string
	value string
}{
	{"well formed", "log_01HXYZ"},
	{"fragment", "log#frag"},
	{"query", "log?replay=1"},
	{"extra segment", "a/b"},
	{"climbs out of the collection", "../stats"},
	{"space", "log one"},
	{"already percent encoded", "log%2Fb"},
}

// TestAPathValueStaysOnePathSegment pins that a value the client puts into a
// URL path occupies exactly one segment on the wire.
//
// It asserts r.RequestURI, NOT r.URL.Path. Go's server has already decoded %2F
// back to a slash by the time it fills URL.Path, so a URL.Path assertion reads
// identically whether or not the client escaped anything — it cannot hold this
// property at all.
func TestAPathValueStaysOnePathSegment(t *testing.T) {
	callers := []struct {
		name   string
		prefix string
		call   func(c *Client, value string)
	}{
		{"Get", "/api/v1/logs/", func(c *Client, value string) {
			_, _ = c.Get(value)
		}},
		{"Group", "/api/v1/logs/group/", func(c *Client, value string) {
			_, _ = c.Group(models.QueryParams{}, value)
		}},
	}

	for _, caller := range callers {
		for _, probe := range pathProbeValues {
			t.Run(caller.name+"/"+probe.name, func(t *testing.T) {
				var got string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got = r.RequestURI
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{}`))
				}))
				defer srv.Close()

				caller.call(New(srv.URL), probe.value)

				want := caller.prefix + url.PathEscape(probe.value)
				if got != want {
					t.Errorf("value %q addressed %q, want %q", probe.value, got, want)
				}
			})
		}
	}
}
