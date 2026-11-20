package onvif

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// HTTP auth for the resources, which ONVIF clients fetch outside of SOAP
// (snapshots). Clients use the same username and password as for the ONVIF
// requests, with the Digest (UniFi Protect, most NVRs) or the Basic scheme.

const (
	httpAuthRealm    = "go2rtc"
	httpAuthNonceAge = 5 * time.Minute
)

var httpAuthSecret = func() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}()

func md5hex(s string) string {
	b := md5.Sum([]byte(s))
	return hex.EncodeToString(b[:])
}

// newNonce - stateless nonce: timestamp + signature
func newNonce(now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 16)
	sum := sha256.Sum256(append([]byte(ts), httpAuthSecret...))
	return ts + "." + hex.EncodeToString(sum[:8])
}

func checkNonce(nonce string, now time.Time) bool {
	ts, _, ok := strings.Cut(nonce, ".")
	if !ok {
		return false
	}
	sec, err := strconv.ParseInt(ts, 16, 64)
	if err != nil {
		return false
	}
	created := time.Unix(sec, 0)
	if now.Before(created.Add(-time.Minute)) || now.After(created.Add(httpAuthNonceAge)) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(nonce), []byte(newNonce(created))) == 1
}

var reDigestParam = regexp.MustCompile(`(\w+)\s*=\s*(?:"([^"]*)"|([^\s,]+))`)

func parseDigest(header string) map[string]string {
	params := map[string]string{}
	for _, m := range reDigestParam.FindAllStringSubmatch(header, -1) {
		if m[2] != "" {
			params[strings.ToLower(m[1])] = m[2]
		} else {
			params[strings.ToLower(m[1])] = m[3]
		}
	}
	return params
}

// CheckHTTPAuth validates the Authorization header (Basic or Digest).
func CheckHTTPAuth(r *http.Request, username, password string, now time.Time) bool {
	header := r.Header.Get("Authorization")

	if strings.HasPrefix(header, "Basic ") {
		user, pass, ok := r.BasicAuth()
		return ok &&
			subtle.ConstantTimeCompare([]byte(user), []byte(username)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1
	}

	if !strings.HasPrefix(header, "Digest ") {
		return false
	}

	p := parseDigest(header[len("Digest "):])
	if p["username"] != username || p["realm"] != httpAuthRealm || !checkNonce(p["nonce"], now) {
		return false
	}
	// the uri should be for this request (ignore proxies that rewrite the host)
	if p["uri"] != r.RequestURI && p["uri"] != r.URL.RequestURI() && p["uri"] != r.URL.Path {
		return false
	}

	ha1 := md5hex(username + ":" + httpAuthRealm + ":" + password)
	ha2 := md5hex(r.Method + ":" + p["uri"])

	var want string
	if p["qop"] == "auth" {
		want = md5hex(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	} else {
		want = md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
	}

	return subtle.ConstantTimeCompare([]byte(p["response"]), []byte(want)) == 1
}

// RequireHTTPAuth sends 401 with the Digest and Basic challenges.
func RequireHTTPAuth(w http.ResponseWriter, now time.Time) {
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(
		`Digest realm="%s", nonce="%s", qop="auth", algorithm=MD5`, httpAuthRealm, newNonce(now),
	))
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, httpAuthRealm))
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
