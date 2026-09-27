// Package httpclient is the one HTTP client every outbound request goes
// through, so each request names YoloCoder and the build that sent it.
//
// Go's default client sends "Go-http-client/1.1" or "Go-http-client/2.0",
// which is what every Go program that never set a User-Agent sends. A
// server or edge rule that turns that agent away would turn YoloCoder
// away with it, and could not tell the two apart when reading its logs.
package httpclient

import (
	"net/http"

	"github.com/mindsdb/yolocoder/internal/version"
)

const homepage = "https://github.com/mindsdb/yolocoder"

// UserAgent is the header every request carries: the product, the version
// the build stamped (main for the rolling release, the tag for a v*
// release, dev for a local build), and where to read about it.
func UserAgent() string {
	return "yolocoder/" + version.Version + " (+" + homepage + ")"
}

// Client behaves like http.DefaultClient except that every request it
// sends, redirects included, carries UserAgent.
var Client = &http.Client{Transport: userAgent{next: http.DefaultTransport}}

// userAgent sets the header on a copy of the request, because a
// RoundTripper must not change the request it was handed.
type userAgent struct {
	next http.RoundTripper
}

func (transport userAgent) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("User-Agent", UserAgent())
	return transport.next.RoundTrip(request)
}
