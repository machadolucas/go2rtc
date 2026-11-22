package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtocolListener(t *testing.T) {
	const (
		unvr            = "[::ffff:192.0.2.40]:50000"
		ha              = "198.51.100.18:50000"
		lan             = "198.51.100.65:50000"
		failover        = "203.0.113.2:50000"
		characteristics = "/api/homekit/characteristics"
		bearer          = "Bearer bridge-token"
	)
	AllowToken(characteristics, "bridge-token")
	t.Cleanup(func() {
		tokensMu.Lock()
		delete(tokens, characteristics)
		tokensMu.Unlock()
	})

	peers := protocolPeers{ONVIF: []string{"192.0.2.40"}}
	peers.HomeKitControl.Peer = "198.51.100.18"
	peers.HomeKitControl.Stream = "eve"

	// Model the shared mux, including a catch-all serving the WebUI.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		require.True(t, IsProtocolRequest(r))
		w.WriteHeader(http.StatusNoContent)
	})
	handler, err := protocolHandler(peers, mux)
	require.NoError(t, err)

	for _, tc := range []struct {
		name, method, path, peer, authorization string
		want                                    int
	}{
		{"HAP setup", "POST", "/pair-setup", lan, "", 204},
		{"HAP verify over alternate interface", "POST", "/pair-verify", failover, "", 204},
		{"HAP method", "GET", "/pair-verify", lan, "", 404},
		{"ONVIF device", "POST", "/onvif/device_service", unvr, "", 204},
		{"ONVIF media", "POST", "/onvif/media_service", unvr, "", 204},
		{"ONVIF events", "POST", "/onvif/events_service", unvr, "", 204},
		{"ONVIF PullPoint", "POST", "/onvif/subscription/abcdef0123456789", unvr, "", 204},
		{"ONVIF snapshot", "GET", "/onvif/snapshot.jpeg?src=eve", unvr, "", 204},
		{"ONVIF peer", "POST", "/onvif/device_service", lan, "", 404},
		{"ONVIF route", "POST", "/onvif/unlisted", unvr, "", 404},
		{"HA reads", "GET", characteristics + "?id=eve", ha, bearer, 204},
		{"HA writes", "PUT", characteristics + "?id=eve", ha, bearer, 204},
		{"HA duplicate IDs", "PUT", characteristics + "?id=eve&id=other", ha, bearer, 404},
		{"HA stream", "GET", characteristics + "?id=other", ha, bearer, 404},
		{"HA method", "POST", characteristics + "?id=eve", ha, bearer, 404},
		{"HA peer", "GET", characteristics + "?id=eve", lan, bearer, 404},
		{"HA token", "GET", characteristics + "?id=eve", ha, "Bearer wrong", 401},
		{"HA token on API", "GET", "/api/streams", ha, bearer, 404},
		{"admin credentials on WebUI", "GET", "/", lan, "Basic YWRtaW46cGFzcw==", 404},
		{"admin credentials on WebSocket", "GET", "/api/ws", lan, "Basic YWRtaW46cGFzcw==", 404},
		{"admin credentials on local protocol port", "GET", "/api/config", "127.0.0.1:50000", "Basic YWRtaW46cGFzcw==", 404},
		{"direct management snapshot", "GET", "/api/frame.jpeg?src=eve", unvr, "", 404},
		{"unlisted encoded route", "POST", "/%70air-verify", lan, "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://bridge:1984"+tc.path, nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("Authorization", tc.authorization)
			req.Header.Set("X-Forwarded-For", "198.51.100.18")
			req.Header.Set("Forwarded", "for=192.0.2.40")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestManagementListener(t *testing.T) {
	handler := managementHandler("admin", "pass", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.False(t, IsProtocolRequest(r))
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, tc := range []struct {
		host, authorization string
		want                int
	}{
		{"127.0.0.1:1985", "", 401},
		{"127.0.0.1:1985", "Bearer bridge-token", 401},
		{"127.0.0.1:1985", "Basic YWRtaW46cGFzcw==", 204},
		{"localhost:1985", "Basic YWRtaW46cGFzcw==", 204},
		{"[::1]:1985", "Basic YWRtaW46cGFzcw==", 204},
		{"bridge.example:1985", "Basic YWRtaW46cGFzcw==", 404},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/api/ws", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", tc.authorization)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, tc.want, rec.Code)
	}

	require.True(t, loopbackListener("127.0.0.1:1985"))
	require.True(t, loopbackListener("[::1]:1985"))
	require.False(t, loopbackListener("0.0.0.0:1985"))
	require.False(t, loopbackListener(":1985"))
	require.False(t, loopbackListener("localhost:1985"))
	require.False(t, loopbackListener("127.0.0.1:0"))
}

func TestMotionAPIManagementOnly(t *testing.T) {
	const (
		unvr   = "192.0.2.40:50000"
		ha     = "198.51.100.18:50000"
		local  = "127.0.0.1:50000"
		bearer = "Bearer bridge-token"
		admin  = "Basic YWRtaW46cGFzcw=="
	)
	AllowToken("/api/homekit/characteristics", "bridge-token")
	t.Cleanup(func() {
		tokensMu.Lock()
		delete(tokens, "/api/homekit/characteristics")
		tokensMu.Unlock()
	})

	peers := protocolPeers{ONVIF: []string{"192.0.2.40"}}
	peers.HomeKitControl.Peer = "198.51.100.18"
	peers.HomeKitControl.Stream = "eve"

	mux := http.NewServeMux()
	mux.HandleFunc("/api/motion", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	protocol, err := protocolHandler(peers, mux)
	require.NoError(t, err)
	management := managementHandler("admin", "pass", mux)

	for _, tc := range []struct {
		name, method, path, peer, authorization string
		want                                    int
	}{
		{"ONVIF peer sets motion", "POST", "/api/motion?src=eve", unvr, "", 404},
		{"ONVIF peer clears motion", "DELETE", "/api/motion?src=eve", unvr, "", 404},
		{"ONVIF peer reads motion", "GET", "/api/motion", unvr, "", 404},
		{"HA token on motion", "POST", "/api/motion?src=eve", ha, bearer, 404},
		{"admin credentials on protocol port", "POST", "/api/motion?src=eve", local, admin, 404},
	} {
		t.Run("protocol "+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://bridge:1984"+tc.path, nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()
			protocol.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code)
		})
	}

	for _, tc := range []struct {
		name, method, authorization string
		want                        int
	}{
		{"sets motion", "POST", admin, 204},
		{"clears motion", "DELETE", admin, 204},
		{"reads motion", "GET", admin, 204},
		{"without credentials", "POST", "", 401},
		{"with the HomeKit token", "POST", bearer, 401},
	} {
		t.Run("management "+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://127.0.0.1:1985/api/motion?src=eve", nil)
			req.RemoteAddr = local
			req.Header.Set("Authorization", tc.authorization)
			rec := httptest.NewRecorder()
			management.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code)
		})
	}
}
