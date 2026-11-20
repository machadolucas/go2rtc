package homekit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/hap"
)

// Watchers keep a long-lived HAP session to a paired HomeKit camera. It is
// separate from the streaming session (which lives only while someone is
// watching) and from the proxy sessions. It is used for:
//   - event notifications (motion sensor, doorbell, light state...)
//   - reading and writing any characteristic via the API
//
// Config:
//
//	homekit_client:
//	  events:
//	    camera1:                           # stream ID with a homekit:// source
//	      webhook: http://192.168.1.2/hook # optional, POST JSON on each event

const (
	watchKeepAlive = 30 * time.Second
	watchTimeout   = 10 * time.Second
	watchRetryMin  = 5 * time.Second
	watchRetryMax  = 60 * time.Second

	// TypeMotionDetected, TypeOccupancyDetected, TypeProgrammableSwitchEvent
	TypeMotionDetected          = "22"
	TypeProgrammableSwitchEvent = "73"
)

// Event is a value change reported by the accessory.
type Event struct {
	Stream  string `json:"stream"`
	AID     uint64 `json:"aid"`
	IID     uint64 `json:"iid"`
	Type    string `json:"type"`    // short type of the characteristic, ex. "22"
	Service string `json:"service"` // short type of the service, ex. "85"
	Value   any    `json:"value"`
}

// CharInfo is a characteristic with the latest known value.
type CharInfo struct {
	AID     uint64   `json:"aid"`
	IID     uint64   `json:"iid"`
	Type    string   `json:"type"`
	Service string   `json:"service"`
	Desc    string   `json:"description,omitempty"`
	Format  string   `json:"format,omitempty"`
	Perms   []string `json:"perms"`
	Value   any      `json:"value,omitempty"`
}

func (c *CharInfo) hasPerm(perm string) bool {
	for _, p := range c.Perms {
		if p == perm {
			return true
		}
	}
	return false
}

type watcher struct {
	stream  string
	webhook string

	mu     sync.Mutex // protects fields below
	client *hap.Client
	chars  []*CharInfo
	index  map[[2]uint64]*CharInfo

	reqMu sync.Mutex // HAP requests can't run in parallel
}

var watchers = map[string]*watcher{}

var listenersMu sync.Mutex
var listeners []func(Event)

// AddEventListener registers a callback for events from all watched cameras.
// The callback must not block.
func AddEventListener(f func(Event)) {
	listenersMu.Lock()
	listeners = append(listeners, f)
	listenersMu.Unlock()
}

// HasWatcher reports if the stream has events from a HomeKit camera.
func HasWatcher(stream string) bool {
	return watchers[stream] != nil
}

// Motion returns the latest state of the first motion sensor of the stream.
func Motion(stream string) (motion, ok bool) {
	w := watchers[stream]
	if w == nil {
		return false, false
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, char := range w.chars {
		if char.Type == TypeMotionDetected {
			return toBool(char.Value), true
		}
	}
	return false, false
}

// normValue - accessories report bool as true/false or 1/0, and a client
// can write any of them
func normValue(v any) any {
	if b, ok := v.(bool); ok {
		if b {
			return float64(1)
		}
		return float64(0)
	}
	return v
}

func toBool(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	}
	return false
}

type watchConfig struct {
	Webhook string `yaml:"webhook"`
}

func initWatchers(conf map[string]watchConfig) {
	if len(conf) == 0 {
		return
	}

	for id, item := range conf {
		if streams.Get(id) == nil {
			log.Warn().Msgf("[homekit] events: missing stream: %s", id)
			continue
		}
		w := &watcher{stream: id, webhook: item.Webhook}
		watchers[id] = w
		go w.run()
	}

	api.HandleFunc("api/homekit/characteristics", apiCharacteristics)
}

func (w *watcher) run() {
	retry := watchRetryMin
	for {
		started := time.Now()
		err := w.session()
		log.Debug().Err(err).Str("stream", w.stream).Msg("[homekit] events: session closed")

		if time.Since(started) > time.Minute {
			retry = watchRetryMin
		}
		time.Sleep(retry)
		if retry *= 2; retry > watchRetryMax {
			retry = watchRetryMax
		}
	}
}

func (w *watcher) url() string {
	stream := streams.Get(w.stream)
	if stream == nil {
		return ""
	}
	return findHomeKitURL(stream.Sources())
}

// session connects to the camera and returns only on error.
func (w *watcher) session() error {
	rawURL := w.url()
	if rawURL == "" {
		return errors.New("not homekit source")
	}

	client, err := hap.Dial(rawURL)
	if err != nil {
		return err
	}
	defer client.Close()

	accs, err := client.GetAccessories()
	if err != nil {
		return err
	}

	var chars []*CharInfo
	var subscribe [][2]uint64
	index := map[[2]uint64]*CharInfo{}

	for _, acc := range accs {
		for _, srv := range acc.Services {
			srvType := hap.ShortType(srv.Type)
			for _, char := range srv.Characters {
				info := &CharInfo{
					AID:     uint64(acc.AID),
					IID:     char.IID,
					Type:    hap.ShortType(char.Type),
					Service: srvType,
					Desc:    char.Desc,
					Format:  char.Format,
					Perms:   char.Perms,
					Value:   char.Value,
				}
				chars = append(chars, info)
				id := [2]uint64{info.AID, info.IID}
				index[id] = info

				// tlv8 and data are setup/config blobs, their events are not
				// interesting and can be big
				if info.hasPerm("ev") && info.Format != hap.FormatTLV8 && info.Format != hap.FormatData {
					subscribe = append(subscribe, id)
				}
			}
		}
	}

	client.OnEvent = func(res *http.Response) {
		items, err := hap.DecodeEvent(res.Body)
		if err != nil {
			return
		}
		for _, item := range items {
			if item.Value != nil {
				w.update(item.AID, item.IID, *item.Value, true)
			}
		}
	}
	client.ListenEvents()

	if len(subscribe) > 0 {
		if err = client.SubscribeCharacters(true, subscribe...); err != nil {
			// some accessories reject part of the list - not fatal
			log.Debug().Err(err).Str("stream", w.stream).Msg("[homekit] events: subscribe")
		}
	}

	w.mu.Lock()
	w.client = client
	w.chars = chars
	w.index = index
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.client = nil
		w.mu.Unlock()
	}()

	log.Info().Str("stream", w.stream).Int("chars", len(chars)).Int("events", len(subscribe)).
		Msg("[homekit] events: connected")

	// The accessory keeps event subscriptions only for a live connection.
	// Polling event characteristics is a keep-alive and also repairs a missed event.
	for {
		time.Sleep(watchKeepAlive)

		if len(subscribe) == 0 {
			if _, err = w.read(client, [][2]uint64{{chars[0].AID, chars[0].IID}}); err != nil {
				return err
			}
			continue
		}

		values, err := w.read(client, subscribe)
		if err != nil {
			return err
		}
		for _, item := range values {
			if item.Value != nil {
				w.update(item.AID, item.IID, *item.Value, true)
			}
		}
	}
}

// read with timeout, because a dead TCP connection can hang forever.
func (w *watcher) read(client *hap.Client, ids [][2]uint64) (values []hap.CharValue, err error) {
	err = w.do(client, func() (err error) {
		values, err = client.ReadCharacters(ids...)
		return
	})
	return
}

func (w *watcher) do(client *hap.Client, f func() error) error {
	w.reqMu.Lock()
	defer w.reqMu.Unlock()

	done := make(chan error, 1)
	go func() { done <- f() }()

	select {
	case err := <-done:
		return err
	case <-time.After(watchTimeout):
		_ = client.Close() // unblocks the request and the session
		return errors.New("homekit: request timeout")
	}
}

// update the cache and fire listeners if the value has changed.
func (w *watcher) update(aid, iid uint64, value any, notify bool) {
	w.mu.Lock()
	info := w.index[[2]uint64{aid, iid}]
	if info == nil {
		w.mu.Unlock()
		return
	}
	// ProgrammableSwitchEvent (doorbell press) is an event even with the same value
	changed := !reflect.DeepEqual(normValue(info.Value), normValue(value)) || info.Type == TypeProgrammableSwitchEvent
	info.Value = value
	event := Event{Stream: w.stream, AID: aid, IID: iid, Type: info.Type, Service: info.Service, Value: value}
	w.mu.Unlock()

	if !changed || !notify {
		return
	}

	log.Debug().Str("stream", w.stream).Uint64("iid", iid).Str("type", event.Type).
		Msgf("[homekit] event value=%v", value)

	listenersMu.Lock()
	for _, f := range listeners {
		f(event)
	}
	listenersMu.Unlock()

	if w.webhook != "" {
		go postWebhook(w.webhook, event)
	}
}

var webhookClient = &http.Client{Timeout: 5 * time.Second}

func postWebhook(url string, event Event) {
	body, err := json.Marshal(event)
	if err != nil {
		return
	}
	res, err := webhookClient.Post(url, api.MimeJSON, bytes.NewReader(body))
	if err != nil {
		log.Debug().Err(err).Msg("[homekit] events: webhook")
		return
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
}

func (w *watcher) conn() (*hap.Client, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.client == nil {
		return nil, errors.New("homekit: camera not connected")
	}
	return w.client, nil
}

// snapshot reads fresh values of all readable characteristics.
func (w *watcher) snapshot() ([]CharInfo, error) {
	client, err := w.conn()
	if err != nil {
		return nil, err
	}

	w.mu.Lock()
	var ids [][2]uint64
	for _, char := range w.chars {
		if char.hasPerm("pr") {
			ids = append(ids, [2]uint64{char.AID, char.IID})
		}
	}
	w.mu.Unlock()

	// accessories limit the size of the request, so read in chunks
	const chunk = 16
	for i := 0; i < len(ids); i += chunk {
		values, err := w.read(client, ids[i:min(i+chunk, len(ids))])
		if err != nil {
			return nil, err
		}
		for _, item := range values {
			if item.Value != nil {
				w.update(item.AID, item.IID, *item.Value, false)
			}
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	items := make([]CharInfo, len(w.chars))
	for i, char := range w.chars {
		items[i] = *char
	}
	return items, nil
}

func (w *watcher) write(values []hap.CharValue) error {
	client, err := w.conn()
	if err != nil {
		return err
	}

	for _, item := range values {
		w.mu.Lock()
		info := w.index[[2]uint64{item.AID, item.IID}]
		w.mu.Unlock()

		switch {
		case info == nil:
			return errors.New("homekit: unknown characteristic")
		case item.Value == nil:
			return errors.New("homekit: empty value")
		case !info.hasPerm("pw"):
			return errors.New("homekit: characteristic is read only")
		}
	}

	if err = w.do(client, func() error {
		_, err := client.WriteCharacters(values...)
		return err
	}); err != nil {
		return err
	}

	for _, item := range values {
		w.update(item.AID, item.IID, *item.Value, true)
	}
	return nil
}

// apiCharacteristics
//
//	GET /api/homekit/characteristics?id=camera1
//	PUT /api/homekit/characteristics?id=camera1
//	    {"characteristics":[{"aid":1,"iid":195,"value":true}]}
func apiCharacteristics(w http.ResponseWriter, r *http.Request) {
	watch := watchers[r.URL.Query().Get("id")]
	if watch == nil {
		http.Error(w, api.StreamNotFound, http.StatusNotFound)
		return
	}

	switch r.Method {
	case "GET":
		items, err := watch.snapshot()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		api.ResponsePrettyJSON(w, struct {
			Value []CharInfo `json:"characteristics"`
		}{items})

	case "PUT", "POST":
		var v struct {
			Value []hap.CharValue `json:"characteristics"`
		}
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil || len(v.Value) == 0 {
			http.Error(w, "wrong request body", http.StatusBadRequest)
			return
		}
		for i := range v.Value {
			if v.Value[i].AID == 0 {
				v.Value[i].AID = hap.DeviceAID
			}
			v.Value[i].Event = nil // subscriptions are managed by the watcher
		}
		if err := watch.write(v.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}
