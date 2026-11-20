# go2rtc fork: full-featured HomeKit cameras

This is a fork of [AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc). The
branch `eve-full` adds what was missing to use a **HomeKit-only camera** (it was
written for an Eve Outdoor Cam) as a complete camera everywhere else: UniFi
Protect, Home Assistant, RTSP clients, and Apple Home at the same time.

`master` follows upstream unchanged. Everything here is meant to be sent
upstream in pieces; nothing has been proposed yet.

## What the branch adds

| Feature | Where | Config / API |
|---|---|---|
| **Long-lived HomeKit session with events.** go2rtc normally talks to the camera only while someone watches. This keeps one extra session open, subscribes to every event-capable characteristic (motion sensor, light state, ...), re-reads them every 30 s and reconnects by itself | `internal/homekit/watch.go`, `pkg/hap/client_chars.go` | `homekit_client.events` |
| **Characteristics API.** Read every characteristic with a fresh value and write the writable ones: flood light, night vision, camera on/off, microphone and speaker volume, ... | same | `GET` / `PUT /api/homekit/characteristics?id=<stream>` |
| **Webhook on change.** Every value change is POSTed as JSON, e.g. to a Home Assistant webhook | same | `homekit_client.events.<stream>.webhook` |
| **ONVIF Events service** (PullPoint) with the motion topics `tns1:RuleEngine/CellMotionDetector/Motion` and `tns1:VideoSource/MotionAlarm`. This is what UniFi Protect 7.1+ reads. Motion comes from the HomeKit motion sensor, or from any other source through the API | `pkg/onvif/events.go`, `internal/onvif/onvif.go` | `POST /api/onvif/motion?src=<stream>&state=true` |
| **ONVIF authentication** (WS-Security UsernameToken) separate from the API password. This is upstream PR [#2231](https://github.com/AlexxIT/go2rtc/pull/2231) by @ssttevee, cherry-picked, plus HTTP 400 on the fault | `pkg/onvif/auth.go` | `onvif.username`, `onvif.password` |
| **HomeKit pairing works with an API password.** `/pair-setup` and `/pair-verify` skip the API Basic auth; HAP has its own pairing and encryption, and Apple devices never send Basic auth | `internal/api/api.go` | none |
| **Two-way audio for HomeKit sources.** Clients send PCMA/PCMU/PCM (WebRTC, RTSP backchannel, the `play audio` API); an external encoder turns it into the AAC-ELD the camera expects | `pkg/homekit/backchannel.go`, `examples/homekit_eldenc` | `homekit_client.talk_encoder` |

## Example config

```yaml
api:
  listen: ":1984"
  username: admin          # Web UI and API
  password: secret

onvif:
  username: protect        # what you type in UniFi Protect when adopting
  password: secret2

streams:
  porch:
    - homekit://192.168.1.50:55465?device_id=...   # written by the pairing API
    - ffmpeg:porch#audio=aac                       # AAC-LC for Protect and RTSP clients

homekit:
  porch:                   # give the camera back to Apple Home (transparent proxy)

homekit_client:
  talk_encoder: /usr/local/bin/eldenc 16000 24000  # optional, two-way audio
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
curl -X POST 'http://127.0.0.1:1984/api/onvif/motion?src=porch&state=true'
```

Webhook body:

```json
{"stream":"porch","aid":1,"iid":162,"type":"22","service":"85","value":true}
```

`type` and `service` are short HomeKit UUIDs: `22` is MotionDetected, `85` the
motion sensor, `25` On, `43` a light bulb, `11B` night vision.

## Two-way audio encoder

HomeKit cameras expect AAC-ELD with 480 samples per frame. go2rtc has no
encoder for it, and ffmpeg's AudioToolbox encoder only makes 512-sample frames.
`talk_encoder` is any command that reads PCM s16le 16 kHz mono on stdin and
writes AAC-ELD frames to stdout, each with a 2-byte big-endian length in front.
[`examples/homekit_eldenc`](examples/homekit_eldenc) is such an encoder for
macOS.

## Status

Tested against one camera, an Eve Outdoor Cam (firmware 1.0.1), on macOS:

- events session, characteristics API, webhook: tested on the camera
- streaming with an AAC-LC transcode (`ffmpeg:<stream>#audio=aac`), including
  audio-only consumers: tested
- two-way audio: tested with the `play audio` API (a tone sent as PCMA was
  played by the camera speaker); not yet tested from a WebRTC client
- ONVIF events and auth: tested with a scripted client that follows the
  request sequence UniFi Protect uses; **not yet tested with a real Protect**
- a real motion event from the camera's sensor: not observed yet

## Build

```bash
go build -o go2rtc .
go test ./pkg/hap/ ./pkg/onvif/ ./internal/api/
```
