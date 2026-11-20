package onvif

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func digestHeader(user, pass, method, uri, nonce string, qop bool) string {
	ha1 := md5hex(user + ":" + httpAuthRealm + ":" + pass)
	ha2 := md5hex(method + ":" + uri)
	if qop {
		res := md5hex(ha1 + ":" + nonce + ":00000001:abcdef:auth:" + ha2)
		return `Digest username="` + user + `", realm="` + httpAuthRealm + `", nonce="` + nonce +
			`", uri="` + uri + `", qop=auth, nc=00000001, cnonce="abcdef", response="` + res + `", algorithm=MD5`
	}
	res := md5hex(ha1 + ":" + nonce + ":" + ha2)
	return `Digest username="` + user + `", realm="` + httpAuthRealm + `", nonce="` + nonce +
		`", uri="` + uri + `", response="` + res + `"`
}

func TestCheckHTTPAuth(t *testing.T) {
	now := time.Now()
	const uri = "/onvif/snapshot.jpeg?src=cam"

	do := func(auth string) bool {
		r := httptest.NewRequest(http.MethodGet, "http://192.168.1.2:1984"+uri, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return CheckHTTPAuth(r, "admin", "pass", now)
	}

	require.False(t, do(""))

	// Basic
	r := httptest.NewRequest(http.MethodGet, "http://192.168.1.2:1984"+uri, nil)
	r.SetBasicAuth("admin", "pass")
	require.True(t, CheckHTTPAuth(r, "admin", "pass", now))
	r.SetBasicAuth("admin", "wrong")
	require.False(t, CheckHTTPAuth(r, "admin", "pass", now))

	// Digest
	nonce := newNonce(now)
	require.True(t, do(digestHeader("admin", "pass", "GET", uri, nonce, true)))
	require.True(t, do(digestHeader("admin", "pass", "GET", uri, nonce, false)))
	require.False(t, do(digestHeader("admin", "wrong", "GET", uri, nonce, true)))
	require.False(t, do(digestHeader("user", "pass", "GET", uri, nonce, true)))
	require.False(t, do(digestHeader("admin", "pass", "GET", "/api/config", nonce, true)))
	require.False(t, do(digestHeader("admin", "pass", "GET", uri, "123.forged", true)))

	// expired nonce
	old := newNonce(now.Add(-time.Hour))
	require.False(t, do(digestHeader("admin", "pass", "GET", uri, old, true)))
}

func TestRequireHTTPAuth(t *testing.T) {
	w := httptest.NewRecorder()
	RequireHTTPAuth(w, time.Now())
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Len(t, w.Header().Values("WWW-Authenticate"), 2)
	require.Contains(t, w.Header().Values("WWW-Authenticate")[0], "Digest ")
}
