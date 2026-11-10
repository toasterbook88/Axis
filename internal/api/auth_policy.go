package api

import (
	"errors"

	"github.com/toasterbook88/axis/internal/auth"
)

// errNoTokenOnTCP is newAPIAuth's refusal of an empty startup token on a TCP
// listener.
var errNoTokenOnTCP = errors.New("no api token on a TCP listener")

// apiAuth is the bearer-token gate shared by every protected route: the token
// source plus the empty-token policy, decided once at startup by
// ServeWithTokenSource and bound into each handler at registration.
type apiAuth struct {
	src auth.TokenSource
	// allowEmpty is F9: an empty token lets requests through unauthenticated.
	// It is true only for a Unix socket (whose file mode is the access
	// control) whose token was empty at startup. Otherwise an empty
	// per-request snapshot is refused with 401, so a source that turns empty
	// after startup can never open a route.
	allowEmpty bool
}

// newAPIAuth decides the empty-token policy for a listener. An empty startup
// token is allowed only on a Unix socket; on TCP it is errNoTokenOnTCP.
func newAPIAuth(src auth.TokenSource, unixSocket bool) (apiAuth, error) {
	empty := src.Current() == ""
	if empty && !unixSocket {
		return apiAuth{}, errNoTokenOnTCP
	}
	return apiAuth{src: src, allowEmpty: empty}, nil
}
