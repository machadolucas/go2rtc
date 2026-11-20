package homekit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/AlexxIT/go2rtc/pkg/hap"
	"github.com/AlexxIT/go2rtc/pkg/hap/camera"
	"github.com/AlexxIT/go2rtc/pkg/hap/tlv8"
)

// Rebroadcast part of the Proxy, see ServerRebroadcast.
//
// JSON is processed as generic maps, because the accessory can have fields
// unknown to the hap.Character and they should reach the controller as is.

func streamingAvailable() string {
	v, _ := tlv8.MarshalBase64(camera.StreamingStatus{Status: camera.StreamingStatusAvailable})
	return v
}

func decodeJSON(body []byte) map[string]any {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber() // don't break big integers
	var v map[string]any
	if d.Decode(&v) != nil {
		return nil
	}
	return v
}

func toUint(v any) uint64 {
	switch v := v.(type) {
	case json.Number:
		n, _ := strconv.ParseUint(v.String(), 10, 64)
		return n
	case float64:
		return uint64(v)
	}
	return 0
}

func (p *Proxy) streamType(iid uint64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streamIDs[iid]
}

// readAccessories should be called before the proxy loops.
func (p *Proxy) readAccessories() error {
	req, err := http.NewRequest("GET", "http://"+p.acc.RemoteAddr().String()+hap.PathAccessories, nil)
	if err != nil {
		return err
	}
	if err = req.Write(p.acc); err != nil {
		return err
	}

	res, err := http.ReadResponse(p.accRd, req)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	p.patchAccessories(body)
	return nil
}

// patchAccessories remembers IDs of the stream characteristics and hides the
// "in use" status, because the stream of the proxy is always available.
func (p *Proxy) patchAccessories(body []byte) []byte {
	v := decodeJSON(body)
	if v == nil {
		return body
	}

	ids := map[uint64]string{}

	accs, _ := v["accessories"].([]any)
	for _, acc := range accs {
		acc, _ := acc.(map[string]any)
		srvs, _ := acc["services"].([]any)
		for _, srv := range srvs {
			srv, _ := srv.(map[string]any)
			chars, _ := srv["characteristics"].([]any)
			for _, char := range chars {
				char, _ := char.(map[string]any)
				typ, _ := char["type"].(string)
				switch typ = hap.ShortType(typ); typ {
				case camera.TypeSetupEndpoints, camera.TypeSelectedStreamConfiguration:
					ids[toUint(char["iid"])] = typ
				case camera.TypeStreamingStatus:
					ids[toUint(char["iid"])] = typ
					char["value"] = streamingAvailable()
				}
			}
		}
	}

	p.mu.Lock()
	p.streamIDs = ids
	p.mu.Unlock()

	if b, err := json.Marshal(v); err == nil {
		return b
	}
	return body
}

// patchCharacteristics hides the "in use" status in the response or event.
// Returns nil if nothing left (event only about the streaming status).
func (p *Proxy) patchCharacteristics(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	v := decodeJSON(body)
	if v == nil {
		return body
	}

	chars, _ := v["characteristics"].([]any)
	changed := false
	for _, char := range chars {
		char, _ := char.(map[string]any)
		if _, ok := char["value"]; ok && p.streamType(toUint(char["iid"])) == camera.TypeStreamingStatus {
			char["value"] = streamingAvailable()
			changed = true
		}
	}

	if !changed {
		return body
	}
	if len(chars) == 1 {
		return nil // response has other handler, so this is only for events
	}
	if b, err := json.Marshal(v); err == nil {
		return b
	}
	return body
}

// handleStream answers the stream requests. Returns nil if the request should
// go to the accessory.
func (p *Proxy) handleStream(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case "GET":
		// GET /characteristics?id=1.55
		query := req.URL.Query()
		ids := strings.Split(query.Get("id"), ",")

		var v hap.JSONCharacters
		for _, id := range ids {
			s1, s2, _ := strings.Cut(id, ".")
			aid, _ := strconv.Atoi(s1)
			iid, _ := strconv.ParseUint(s2, 10, 64)

			var value any
			switch p.streamType(iid) {
			case camera.TypeSetupEndpoints:
				p.mu.Lock()
				answer := p.answer
				p.mu.Unlock()
				if answer == "" {
					return nil, nil
				}
				value = answer
			case camera.TypeStreamingStatus:
				value = streamingAvailable()
			case camera.TypeSelectedStreamConfiguration:
				// the accessory knows nothing about this session
				p.mu.Lock()
				value = p.selected
				p.mu.Unlock()
			default:
				// request with other characteristics - response will be patched
				return nil, nil
			}

			v.Value = append(v.Value, hap.JSONCharacter{AID: uint8(aid), IID: iid, Value: value})
		}

		return makeResponse(hap.MimeJSON, v)

	case "PUT":
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))

		var v struct {
			Value []struct {
				IID   uint64 `json:"iid"`
				Value any    `json:"value"`
			} `json:"characteristics"`
		}
		if json.Unmarshal(body, &v) != nil || len(v.Value) == 0 {
			return nil, nil
		}

		// the stream setup is always a separate request with value
		for _, char := range v.Value {
			if char.Value == nil {
				return nil, nil
			}
			if typ := p.streamType(char.IID); typ != camera.TypeSetupEndpoints && typ != camera.TypeSelectedStreamConfiguration {
				return nil, nil
			}
		}

		for _, char := range v.Value {
			switch p.streamType(char.IID) {
			case camera.TypeSetupEndpoints:
				var offer camera.SetupEndpointsRequest
				if err = tlv8.UnmarshalBase64(char.Value, &offer); err != nil {
					return nil, err
				}

				consumer := p.rebroadcast.NewConsumer(p.con)
				if consumer == nil {
					return nil, nil // rebroadcast disabled
				}
				consumer.SetOffer(&offer)

				// the answer has random keys, so it should be generated once
				answer, err := tlv8.MarshalBase64(consumer.GetAnswer())
				if err != nil {
					return nil, err
				}

				p.mu.Lock()
				p.pending = consumer
				p.answer = answer
				p.mu.Unlock()

			case camera.TypeSelectedStreamConfiguration:
				var conf camera.SelectedStreamConfiguration
				if err = tlv8.UnmarshalBase64(char.Value, &conf); err != nil {
					return nil, err
				}
				p.selectStream(&conf)

				p.mu.Lock()
				p.selected, _ = char.Value.(string)
				p.mu.Unlock()
			}
		}

		res := &http.Response{
			StatusCode: http.StatusNoContent,
			Proto:      "HTTP",
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		return res, nil
	}

	return nil, nil
}

func (p *Proxy) selectStream(conf *camera.SelectedStreamConfiguration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch conf.Control.Command {
	case camera.SessionCommandStart:
		consumer := p.pending
		if consumer == nil || !consumer.SetConfig(conf) {
			return
		}
		p.pending = nil

		if err := p.rebroadcast.StartConsumer(consumer); err != nil {
			_ = consumer.Stop()
			return
		}
		p.consumers = append(p.consumers, consumer)

	case camera.SessionCommandEnd:
		for i, consumer := range p.consumers {
			if consumer.SessionID() == conf.Control.SessionID {
				_ = consumer.Stop()
				p.consumers = append(p.consumers[:i], p.consumers[i+1:]...)
				return
			}
		}
	}
}

func (p *Proxy) stopConsumers() {
	p.mu.Lock()
	for _, consumer := range p.consumers {
		_ = consumer.Stop()
	}
	p.consumers = nil
	p.pending = nil
	p.mu.Unlock()
}
