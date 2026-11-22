# go2rtc fork: full-featured HomeKit cameras

This is a fork of [AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc). The
branch `eve-full` adds what was missing to use a **HomeKit-only camera** (it was
written for an Eve Outdoor Cam) as a complete camera everywhere else: UniFi
Protect, Home Assistant, RTSP clients, and Apple Home at the same time.

`master` follows upstream unchanged. Everything here is meant to be sent
upstream in pieces. ONVIF authentication is upstream's (PR
[#2231](https://github.com/AlexxIT/go2rtc/pull/2231), merged). The stream
motion state and the ONVIF Events service are proposed as PR
[#2722](https://github.com/AlexxIT/go2rtc/pull/2722); this branch carries
that PR's code.

## What the branch adds

| Feature | Where | Config / API |
|---|---|---|
| **Long-lived HomeKit session with events.** go2rtc normally talks to the camera only while someone watches. This keeps one extra session open, subscribes to every event-capable characteristic (motion sensor, light state, ...), re-reads them every 30 s and reconnects by itself. The camera's MotionDetected value feeds the stream motion state (below), at connect and on every change | `internal/homekit/watch.go`, `pkg/hap/client_chars.go` | `homekit_client.events` |
| **Characteristics API.** Read every characteristic with a fresh value and write the writable ones: flood light, night vision, camera on/off, microphone and speaker volume, ... | same | `GET` / `PUT /api/homekit/characteristics?id=<stream>` |
| **Scoped token** for the characteristics API, so a client can control the camera without the API password | `internal/api/api.go` | `homekit_client.token` |
| **Webhook on change.** Every value change is POSTed as JSON, e.g. to a Home Assistant webhook | same | `homekit_client.events.<stream>.webhook` |
| **Motion state of streams.** One protocol-neutral motion state per stream. The HomeKit events session sets it; any other detector can set it through the API. Modules report it to their clients (the ONVIF Events service). From PR #2722 | `internal/streams/motion.go` | `POST` / `DELETE` / `GET /api/motion?src=<stream>` |
| **ONVIF Events service** (PullPoint) with the motion topics `tns1:RuleEngine/CellMotionDetector/Motion` and `tns1:VideoSource/MotionAlarm`. This is what UniFi Protect 7.1+ reads. Events come from the stream motion state. Always advertised in `GetCapabilities` and `GetServices`. From PR #2722 | `pkg/onvif/server_events.go`, `internal/onvif/onvif.go` | none |
| **ONVIF authentication** (WS-Security UsernameToken) is upstream's: the RTSP username and password, else the API ones. `GetSystemDateAndTime`, `GetCapabilities`, `GetServices`, `GetServiceCapabilities` and `GetHostname` need no login. The fork answers the NotAuthorized fault with HTTP 400 and a SOAP content type (upstream: 401), as UniFi Protect expects | `pkg/onvif/auth.go`, `internal/onvif/onvif.go` | `rtsp.username`, `rtsp.password` |
| **Audio in the ONVIF profiles** (AAC `AudioEncoderConfiguration`, audio sources), opt-in. Without it UniFi Protect treats the camera as having no microphone | `pkg/onvif/server.go` | `onvif.audio` |
| **HomeKit pairing works with an API password.** `/pair-setup` and `/pair-verify` skip the API Basic auth; HAP has its own pairing and encryption, and Apple devices never send Basic auth | `internal/api/api.go` | none |
| **Proxy rebroadcast.** A HomeKit camera streams to one or two clients only, and go2rtc is already one. With rebroadcast the Apple Home proxy answers live-view requests itself and sends the stream it already receives (H.264 and AAC-ELD untouched, no transcoding), in both directions for two-way audio. Any number of Apple devices can watch while an NVR records. Controls, events, snapshots and HKSV still go to the camera | `pkg/homekit/proxy_stream.go` | `homekit.<stream>.rebroadcast: true` |
| **Snapshots with the ONVIF login** (HTTP Digest or Basic) at `/onvif/snapshot.jpeg`, checked against the same credentials as the ONVIF auth and returned by `GetSnapshotUri` whenever ONVIF auth is active | `pkg/onvif/httpauth.go` | none |
| **Loopback management listener.** The full WebUI and API move to a numeric loopback address with Basic auth on every request; the LAN listener serves only HAP pairing, ONVIF (SOAP, events, snapshots) to listed peers and the characteristics API to one peer with the token. `/api/motion` is management only. See [`internal/api/README.md`](internal/api/README.md) | `internal/api/protocols.go` | `api.management_listen`, `api.protocol_peers` |
| **Fixes for the HomeKit source**: audio-only consumers, a consumer that asks for video after the camera was started for audio, chunked responses | `pkg/homekit`, `pkg/hap` | none |
| **Two-way audio for HomeKit sources.** Clients send PCMA/PCMU/PCM (WebRTC, RTSP backchannel, the `play audio` API); an external encoder turns it into the AAC-ELD the camera expects | `pkg/homekit/backchannel.go`, `examples/homekit_eldenc` | `homekit_client.talk_encoder` |

## Example config

```yaml
api:
  listen: ":1984"
  username: admin          # Web UI and API
  password: secret

onvif:
  audio: true              # advertise AAC audio in the ONVIF profiles (Protect needs it)

rtsp:
  username: protect        # also the ONVIF login: what you type in UniFi Protect when adopting
  password: secret2
  default_query: "video&audio=aac"   # give RTSP clients AAC instead of the camera's AAC-ELD

streams:
  porch:
    - homekit://192.168.1.50:55465?device_id=...   # written by the pairing API
    - ffmpeg:porch#audio=aac                       # AAC-LC for Protect and RTSP clients

homekit:
  porch:                   # give the camera back to Apple Home (transparent proxy)
    rebroadcast: true      # live view from go2rtc's stream, not a second camera stream

homekit_client:
  talk_encoder: /usr/local/bin/eldenc 16000 24000  # optional, two-way audio
  token: long-random-string    # optional, see below
  events:
    porch:
      webhook: http://192.168.1.2:8123/api/webhook/porch_camera   # optional
```

## API examples

```bash
# all characteristics with current values
curl 'http://127.0.0.1:1984/api/homekit/characteristics?id=porch'

# switch the flood light on (aid/iid come from the list above)
curl -X PUT 'http://127.0.0.1:1984/api/homekit/characteristics?id=porch' \
  -d '{"characteristics":[{"aid":1,"iid":195,"value":true}]}'

# report motion from another detector to ONVIF clients
curl -X POST 'http://127.0.0.1:1984/api/motion?src=porch'     # started
curl -X DELETE 'http://127.0.0.1:1984/api/motion?src=porch'   # ended
curl 'http://127.0.0.1:1984/api/motion'                       # {"porch":false}
```

With `api.management_listen`, call `/api/motion` on the management address;
the LAN listener doesn't serve it.

With `homekit_client.token` set, `/api/homekit/characteristics` also accepts
`Authorization: Bearer <token>` instead of the API username and password. The
token opens nothing else, so it can be given to Home Assistant.

Webhook body:

```json
{"stream":"porch","aid":1,"iid":162,"type":"22","service":"85","value":true}
```

`type` and `service` are short HomeKit UUIDs: `22` is MotionDetected, `85` the
motion sensor, `25` On, `43` a light bulb, `11B` night vision.

## Config changes since the first branch

- removed: `onvif.username`, `onvif.password`. ONVIF uses `rtsp.username`
  and `rtsp.password` (or the API login when RTSP has none). Clients that
  used the old ONVIF login need it to be the RTSP login.
- removed: `POST /api/onvif/motion?src=<stream>&state=true|false`. Use
  `POST` (motion) and `DELETE` (no motion) on `/api/motion?src=<stream>`.

## Two-way audio encoder

HomeKit cameras expect AAC-ELD with 480 samples per frame. go2rtc has no
encoder for it, and ffmpeg's AudioToolbox encoder only makes 512-sample frames.
`talk_encoder` is any command that reads PCM s16le 16 kHz mono on stdin and
writes AAC-ELD frames to stdout, each with a 2-byte big-endian length in front.
[`examples/homekit_eldenc`](examples/homekit_eldenc) is such an encoder for
macOS.

## Status

The branch merged with upstream (stream motion state, upstream's ONVIF
auth) is tested with unit tests and a local smoke test on loopback ports
(ONVIF auth with the RTSP login, events advertised, `/api/motion` to a
PullPoint `Changed` message, `/api/motion` and `/api/streams` not served on
the protocol listener, snapshot 401 without credentials). It is not yet
tested against the camera, UniFi Protect or Apple Home; the list below is
for the branch before that merge. `pkg/rtsp` tests fail intermittently
(`TestMissedControl`) and under `-race` (`TestBackchannelSeparateSession`,
`TestBackchannelFallback`) in upstream code, before and after the merge.

Tested against one camera, an Eve Outdoor Cam (firmware 1.0.1), on macOS:

- events session, characteristics API, webhook: tested on the camera
- streaming with an AAC-LC transcode (`ffmpeg:<stream>#audio=aac`), including
  audio-only consumers: tested
- two-way audio: tested with the `play audio` API (a tone sent as PCMA was
  played by the camera speaker); not yet tested from a WebRTC client
- proxy rebroadcast: works with Apple devices (Home app live view and two-way
  audio) while UniFi Protect records the same camera; needs the AES_256 SRTP
  support of this branch, because Apple devices select it
- UniFi Protect: adoption by `host:1984` with the ONVIF login, video, audio
  (`onvif.audio`, the AAC default query and "Stream Compatibility Mode:
  Improved" in Protect) and motion events work; snapshots with the ONVIF login
  are tested with curl only

## Build

```bash
go build -o go2rtc .
go test ./internal/api/... ./internal/streams/ ./internal/onvif/ ./internal/homekit/ \
  ./internal/mjpeg/ ./pkg/onvif/ ./pkg/hap/... ./pkg/homekit/ ./pkg/srtp/ ./pkg/rtsp/
```
