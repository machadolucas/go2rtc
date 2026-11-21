package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/AlexxIT/go2rtc/pkg/hap"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

const ONVIFSnapshotPath = "/onvif/snapshot.jpeg"

type protocolPeers struct {
	ONVIF          []string `yaml:"onvif"`
	HomeKitControl struct {
		Peer   string `yaml:"peer"`
		Stream string `yaml:"stream"`
	} `yaml:"homekit_control"`
}

type protocolRequestKey struct{}

// IsProtocolRequest distinguishes protocol clients from full API clients.
func IsProtocolRequest(r *http.Request) bool {
	return r.Context().Value(protocolRequestKey{}) == true
}

func protocolHandler(peers protocolPeers, next http.Handler) (http.Handler, error) {
	onvifPeers := make(map[netip.Addr]bool, len(peers.ONVIF))
	for _, peer := range peers.ONVIF {
		addr, err := netip.ParseAddr(peer)
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("onvif peers must be IP addresses")
		}
		onvifPeers[addr.Unmap()] = true
	}

	control := peers.HomeKitControl
	var controlPeer netip.Addr
	if control.Peer != "" || control.Stream != "" {
		addr, err := netip.ParseAddr(control.Peer)
		if err != nil || addr.Zone() != "" || control.Stream == "" {
			return nil, fmt.Errorf("homekit_control requires a peer IP address and stream")
		}
		controlPeer = addr.Unmap()
	}
	characteristicsPath := basePath + "/api/homekit/characteristics"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match literal protocol paths before ServeMux can clean or redirect them.
		if r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}

		peer, _ := netip.ParseAddrPort(r.RemoteAddr)
		addr := peer.Addr().Unmap()
		path := r.URL.Path
		switch {
		case path == hap.PathPairSetup || path == hap.PathPairVerify:
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
		case path == characteristicsPath:
			ids := r.URL.Query()["id"]
			if !controlPeer.IsValid() || addr != controlPeer || len(ids) != 1 || ids[0] != control.Stream ||
				(r.Method != http.MethodGet && r.Method != http.MethodPut) {
				http.NotFound(w, r)
				return
			}
			if !checkToken(r) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		case isONVIFProtocolRequest(r):
			if !onvifPeers[addr] {
				http.NotFound(w, r)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), protocolRequestKey{}, true)))
	}), nil
}

func isONVIFProtocolRequest(r *http.Request) bool {
	if r.URL.Path == ONVIFSnapshotPath {
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case onvif.PathDevice, "/onvif/media_service", onvif.PathEvents:
		return true
	}
	id, ok := strings.CutPrefix(r.URL.Path, onvif.PathSubscription)
	var decoded [8]byte
	if !ok || len(id) != hex.EncodedLen(len(decoded)) {
		return false
	}
	_, err := hex.Decode(decoded[:], []byte(id))
	return err == nil
}

func loopbackListener(address string) bool {
	addr, err := netip.ParseAddrPort(address)
	return err == nil && addr.Addr().IsLoopback() && addr.Port() != 0
}

func managementHandler(username, password string, next http.Handler) http.Handler {
	auth := requireBasicAuth(username, password, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep the loopback API out of virtual hosts, including DNS aliases
		// that can change their address after a browser has loaded a page.
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		addr, _ := netip.ParseAddr(strings.Trim(host, "[]"))
		if !strings.EqualFold(host, "localhost") && !addr.IsLoopback() {
			http.NotFound(w, r)
			return
		}
		auth.ServeHTTP(w, r)
	})
}
