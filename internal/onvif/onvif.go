package onvif

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/homekit"
	"github.com/AlexxIT/go2rtc/internal/rtsp"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
	"github.com/rs/zerolog"
)

func Init() {
	log = app.GetLogger("onvif")

	var cfg struct {
		Mod struct {
			Audio bool `yaml:"audio"`
		} `yaml:"onvif"`
		API struct {
			Username string `yaml:"username"`
			Password string `yaml:"password"`
		} `yaml:"api"`
		RTSP struct {
			Username string `yaml:"username"`
			Password string `yaml:"password"`
		} `yaml:"rtsp"`
	}
	app.LoadConfig(&cfg)

	// ONVIF auth: RTSP credentials, else API credentials. The same login is
	// used for the snapshot endpoint and by clients for the RTSP stream.
	if cfg.RTSP.Username != "" {
		authUsername, authPassword = cfg.RTSP.Username, cfg.RTSP.Password
	} else if cfg.API.Username != "" {
		authUsername, authPassword = cfg.API.Username, cfg.API.Password
	}
	if authUsername != "" {
		auth = onvif.NewAuth(authUsername, authPassword)
	}
	audio = cfg.Mod.Audio

	streams.HandleFunc("onvif", streamOnvif)

	// motion events from HomeKit cameras => ONVIF Events service
	for _, name := range streams.GetAllNames() {
		if homekit.HasWatcher(name) {
			events.AddSource(name)
		}
	}
	homekit.AddEventListener(func(event homekit.Event) {
		if event.Type == homekit.TypeMotionDetected {
			motion, _ := homekit.Motion(event.Stream)
			events.SetMotion(event.Stream, motion)
		}
	})

	// motion events from any external source
	api.HandleFunc("api/onvif/motion", apiMotion)

	// ONVIF server on all suburls
	api.HandleFuncNoAuth("/onvif/", onvifDeviceService)

	// snapshots with the ONVIF auth (HTTP Digest or Basic), not the API auth
	api.HandleFuncNoAuth(api.ONVIFSnapshotPath, onvifSnapshot)

	// ONVIF client autodiscovery
	api.HandleFunc("api/onvif", apiOnvif)
}

var log zerolog.Logger
var auth *onvif.Auth
var authUsername string
var authPassword string
var events = onvif.NewEvents()
var audio bool // advertise AAC audio in the profiles

func streamOnvif(rawURL string) (core.Producer, error) {
	client, err := onvif.NewClient(rawURL)
	if err != nil {
		return nil, err
	}

	uri, err := client.GetURI()
	if err != nil {
		return nil, err
	}

	// Append hash-based arguments to the retrieved URI
	if i := strings.IndexByte(rawURL, '#'); i > 0 {
		uri += rawURL[i:]
	}

	log.Debug().Msgf("[onvif] new uri=%s", uri)

	if err = streams.Validate(uri); err != nil {
		return nil, err
	}

	return streams.GetProducer(uri)
}

func onvifDeviceService(w http.ResponseWriter, r *http.Request) {
	protocolRequest := api.IsProtocolRequest(r)
	if protocolRequest && (authUsername == "" || authPassword == "") {
		http.Error(w, "ONVIF credentials required", http.StatusServiceUnavailable)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	operation := onvif.GetRequestAction(b)
	if operation == "" {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}

	log.Trace().Msgf("[onvif] server request %s %s:\n%s", r.Method, r.RequestURI, b)

	if auth != nil {
		switch operation {
		case onvif.DeviceGetSystemDateAndTime:
		case onvif.DeviceGetServices:
		case onvif.DeviceGetCapabilities:
		case onvif.ServiceGetServiceCapabilities:
		case onvif.DeviceGetHostname:
		default:
			if !auth.Validate(b) {
				log.Debug().Str("addr", r.RemoteAddr).Str("operation", operation).Msg("[onvif] auth failed")
				// SOAP fault should have HTTP status 400, important for UniFi Protect
				w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write(onvif.NotAuthorizedResponse())
				return
			}
		}
	}

	status := http.StatusOK

	switch operation {
	case onvif.ServiceGetServiceCapabilities:
		if strings.HasSuffix(r.URL.Path, onvif.PathEvents) {
			b = onvif.EventsServiceCapabilitiesResponse()
		} else {
			b = onvif.StaticResponse(operation) // important for Hass
		}

	case onvif.EventsGetEventProperties:
		b = onvif.GetEventPropertiesResponse()

	case onvif.EventsCreatePullPointSubscription:
		ttl := onvif.ParseDuration(onvif.FindTagValue(b, "InitialTerminationTime"), time.Now())
		id, expires, ok := events.Subscribe(ttl)
		if !ok {
			http.Error(w, "too many subscriptions", http.StatusServiceUnavailable)
			return
		}
		b = onvif.CreatePullPointSubscriptionResponse(r.Host, id, expires)

	case onvif.EventsPullMessages:
		timeout := onvif.ParseDuration(onvif.FindTagValue(b, "Timeout"), time.Now())
		limit, _ := strconv.Atoi(onvif.FindTagValue(b, "MessageLimit"))
		if b = events.Pull(onvif.SubscriptionID(r.URL.Path), timeout, limit); b == nil {
			b = onvif.ResourceUnknownResponse()
			status = http.StatusBadRequest
		}

	case onvif.EventsRenew:
		ttl := onvif.ParseDuration(onvif.FindTagValue(b, "TerminationTime"), time.Now())
		if expires, ok := events.Renew(onvif.SubscriptionID(r.URL.Path), ttl); ok {
			b = onvif.RenewResponse(expires)
		} else {
			b = onvif.ResourceUnknownResponse()
			status = http.StatusBadRequest
		}

	case onvif.EventsUnsubscribe:
		events.Unsubscribe(onvif.SubscriptionID(r.URL.Path))
		b = onvif.UnsubscribeResponse()

	case onvif.EventsSetSynchronizationPoint:
		events.Sync(onvif.SubscriptionID(r.URL.Path))
		b = onvif.SetSynchronizationPointResponse()

	case onvif.DeviceGetNetworkInterfaces, // important for Hass
		onvif.DeviceGetSystemDateAndTime, // important for Hass
		onvif.DeviceSetSystemDateAndTime, // return just OK
		onvif.DeviceGetDiscoveryMode,
		onvif.DeviceGetDNS,
		onvif.DeviceGetHostname,
		onvif.DeviceGetNetworkDefaultGateway,
		onvif.DeviceGetNetworkProtocols,
		onvif.DeviceGetNTP,
		onvif.DeviceGetScopes,
		onvif.MediaGetVideoEncoderConfiguration,
		onvif.MediaGetVideoEncoderConfigurations,
		onvif.MediaGetVideoEncoderConfigurationOptions:
		b = onvif.StaticResponse(operation)

	case onvif.MediaGetAudioEncoderConfigurations:
		if audio {
			b = onvif.GetAudioEncoderConfigurationsResponse()
		} else {
			b = onvif.StaticResponse(operation)
		}

	case onvif.MediaGetAudioSources:
		if audio {
			b = onvif.GetAudioSourcesResponse(streams.GetAllNames())
		} else {
			b = onvif.StaticResponse(operation)
		}

	case onvif.MediaGetAudioSourceConfigurations:
		if audio {
			b = onvif.GetAudioSourceConfigurationsResponse(streams.GetAllNames())
		} else {
			b = onvif.StaticResponse(operation)
		}

	case onvif.DeviceGetCapabilities:
		// important for Hass: Media section
		b = onvif.GetCapabilitiesResponse(r.Host, events.HasSources())

	case onvif.DeviceGetServices:
		b = onvif.GetServicesResponse(r.Host, events.HasSources())

	case onvif.DeviceGetDeviceInformation:
		// important for Hass: SerialNumber (unique server ID)
		b = onvif.GetDeviceInformationResponse("", "go2rtc", app.Version, r.Host)

	case onvif.DeviceSystemReboot:
		if protocolRequest {
			http.Error(w, "unsupported operation", http.StatusBadRequest)
			return
		}
		b = onvif.StaticResponse(operation)

		time.AfterFunc(time.Second, func() {
			os.Exit(0)
		})

	case onvif.MediaGetVideoSources:
		b = onvif.GetVideoSourcesResponse(streams.GetAllNames())

	case onvif.MediaGetProfiles:
		// important for Hass: H264 codec, width, height
		b = onvif.GetProfilesResponse(streams.GetAllNames(), audio)

	case onvif.MediaGetProfile:
		token := onvif.FindTagValue(b, "ProfileToken")
		b = onvif.GetProfileResponse(token, audio)

	case onvif.MediaGetVideoSourceConfigurations:
		// important for Happytime Onvif Client
		b = onvif.GetVideoSourceConfigurationsResponse(streams.GetAllNames())

	case onvif.MediaGetVideoSourceConfiguration:
		token := onvif.FindTagValue(b, "ConfigurationToken")
		b = onvif.GetVideoSourceConfigurationResponse(token)

	case onvif.MediaGetStreamUri:
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host // in case of Host without port
		}

		uri := "rtsp://" + host + ":" + rtsp.Port + "/" + onvif.FindTagValue(b, "ProfileToken")
		b = onvif.GetStreamUriResponse(uri)

	case onvif.MediaGetSnapshotUri:
		// clients fetch the snapshot with the ONVIF username and password
		path := "/api/frame.jpeg"
		if auth != nil {
			path = api.ONVIFSnapshotPath
		}
		uri := "http://" + r.Host + path + "?src=" + onvif.FindTagValue(b, "ProfileToken")
		b = onvif.GetSnapshotUriResponse(uri)

	default:
		http.Error(w, "unsupported operation", http.StatusBadRequest)
		log.Warn().Msgf("[onvif] unsupported operation: %s", operation)
		log.Debug().Msgf("[onvif] unsupported request:\n%s", b)
		return
	}

	log.Trace().Msgf("[onvif] server response:\n%s", b)

	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(status)
	if _, err = w.Write(b); err != nil {
		log.Error().Err(err).Caller().Send()
	}
}

func apiOnvif(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")

	var items []*api.Source

	if src == "" {
		devices, err := onvif.DiscoveryStreamingDevices()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for _, device := range devices {
			u, err := url.Parse(device.URL)
			if err != nil {
				log.Warn().Str("url", device.URL).Msg("[onvif] broken")
				continue
			}

			if u.Scheme != "http" {
				log.Warn().Str("url", device.URL).Msg("[onvif] unsupported")
				continue
			}

			u.Scheme = "onvif"
			u.User = url.UserPassword("user", "pass")

			if u.Path == onvif.PathDevice {
				u.Path = ""
			}

			items = append(items, &api.Source{
				Name: u.Host,
				URL:  u.String(),
				Info: device.Name + " " + device.Hardware,
			})
		}
	} else {
		client, err := onvif.NewClient(src)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if l := log.Trace(); l.Enabled() {
			b, _ := client.MediaRequest(onvif.MediaGetProfiles)
			l.Msgf("[onvif] src=%s profiles:\n%s", src, b)
		}

		name, err := client.GetName()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		tokens, err := client.GetProfilesTokens()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for i, token := range tokens {
			items = append(items, &api.Source{
				Name: name + " stream" + strconv.Itoa(i),
				URL:  src + "?subtype=" + token,
			})
		}

		if len(tokens) > 0 && client.HasSnapshots() {
			items = append(items, &api.Source{
				Name: name + " snapshot",
				URL:  src + "?subtype=" + tokens[0] + "&snapshot",
			})
		}
	}

	api.ResponseSources(w, items)
}

// apiMotion - set motion state for the ONVIF Events service from any source
//
//	POST /api/onvif/motion?src=camera1&state=true
func apiMotion(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	src := query.Get("src")
	if streams.Get(src) == nil {
		http.Error(w, api.StreamNotFound, http.StatusNotFound)
		return
	}

	switch query.Get("state") {
	case "true", "1", "on":
		events.SetMotion(src, true)
	case "false", "0", "off":
		events.SetMotion(src, false)
	default:
		http.Error(w, "wrong state", http.StatusBadRequest)
	}
}

// onvifSnapshot - the same as api/frame.jpeg, but with the ONVIF username and
// password (HTTP Digest or Basic) instead of the API auth.
func onvifSnapshot(w http.ResponseWriter, r *http.Request) {
	protocolRequest := api.IsProtocolRequest(r)
	if protocolRequest && (authUsername == "" || authPassword == "") {
		http.Error(w, "ONVIF credentials required", http.StatusServiceUnavailable)
		return
	}
	if auth != nil && !onvif.CheckHTTPAuth(r, authUsername, authPassword, time.Now()) {
		onvif.RequireHTTPAuth(w, time.Now())
		return
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = "/api/frame.jpeg"
	if protocolRequest {
		src := r.URL.Query().Get("src")
		r2.URL.RawQuery = url.Values{"src": {src}}.Encode()
	}
	r2.RequestURI = r2.URL.RequestURI()

	handler, pattern := http.DefaultServeMux.Handler(r2)
	if pattern == "" {
		http.Error(w, "snapshots not supported", http.StatusNotFound)
		return
	}
	handler.ServeHTTP(w, r2)
}
