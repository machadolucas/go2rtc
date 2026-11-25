package homekit

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/pcm"
	"github.com/AlexxIT/go2rtc/pkg/shell"
	"github.com/pion/rtp"
)

// Two-way audio (talkback) for HomeKit cameras.
//
// HomeKit uses the same SRTP audio session in both directions and the camera
// expects the codec from the selected stream configuration. Usually it is
// AAC-ELD 16000 with 480 samples per frame. go2rtc has no encoder for it, so an
// external command is used:
//
//	stdin:  PCM s16le 16000 Hz mono
//	stdout: AAC-ELD frames, each prefixed with a 2 byte big endian length
//
// Clients (ex. WebRTC) send PCMA/PCMU, which is transcoded to PCM here.

const (
	talkSampleRate    = 16000
	talkFrameSamples  = 480
	talkPayloadType   = 110 // same as in the camera.NewStream
	talkMaxFrameBytes = 2048
)

// talkMedia - AAC-ELD is passed to the camera as is (from a HomeKit
// controller). Other codecs need the external encoder.
func talkMedia(encoder bool) *core.Media {
	media := &core.Media{
		Kind:      core.KindAudio,
		Direction: core.DirectionSendonly,
		Codecs: []*core.Codec{
			{Name: core.CodecELD, ClockRate: talkSampleRate},
		},
	}
	if encoder {
		media.Codecs = append(media.Codecs,
			&core.Codec{Name: core.CodecPCMA, ClockRate: 8000},
			&core.Codec{Name: core.CodecPCMU, ClockRate: 8000},
			&core.Codec{Name: core.CodecPCML, ClockRate: talkSampleRate},
			&core.Codec{Name: core.CodecPCM, ClockRate: talkSampleRate},
		)
	}
	return media
}

func (c *Client) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	if track.Codec.Name == core.CodecELD {
		return c.addTalkPassthrough(media, track)
	}

	if c.TalkCommand == "" {
		return errors.New("homekit: talk encoder not configured")
	}

	c.talkMu.Lock()
	defer c.talkMu.Unlock()

	if c.talkCmd != nil {
		return errors.New("homekit: talk is busy")
	}

	cmd := shell.NewCommand(c.TalkCommand)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	c.talkCmd = cmd

	go c.talkReader(stdout)

	dst := &core.Codec{Name: core.CodecPCML, ClockRate: talkSampleRate, Channels: 1}
	transcode := pcm.Transcode(dst, track.Codec)

	sender := core.NewSender(media, track.Codec)
	sender.Handler = func(packet *rtp.Packet) {
		_, _ = stdin.Write(transcode(packet.Payload))
	}
	sender.HandleRTP(track)
	c.Senders = append(c.Senders, sender)

	return nil
}

// addTalkPassthrough sends AAC-ELD from the consumer to the camera as is.
func (c *Client) addTalkPassthrough(media *core.Media, track *core.Receiver) error {
	var seq uint16

	sender := core.NewSender(media, track.Codec)
	sender.Handler = func(packet *rtp.Packet) {
		c.talkMu.Lock()
		session := c.audioSession
		c.talkMu.Unlock()

		// before the Start and after the Stop
		if session == nil || session.Remote == nil {
			return
		}

		seq++
		clone := &rtp.Packet{
			Header:  rtp.Header{Marker: packet.Marker, SequenceNumber: seq, Timestamp: packet.Timestamp},
			Payload: packet.Payload,
		}
		if n, err := session.WriteRTP(clone); err == nil {
			c.Send += n
		}
	}
	sender.HandleRTP(track)
	c.Senders = append(c.Senders, sender)
	return nil
}

// talkReader reads frames from the encoder and sends them to the camera.
func (c *Client) talkReader(r io.Reader) {
	var size [2]byte
	var seq uint16
	var ts uint32

	for {
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(size[:]))
		if n == 0 || n > talkMaxFrameBytes {
			return
		}

		// RFC 3640: AU-headers-length (16 bits), AU-header (13 bits size + 3 bits index)
		payload := make([]byte, 4+n)
		payload[0] = 0x00
		payload[1] = 0x10
		binary.BigEndian.PutUint16(payload[2:], uint16(n)<<3)
		if _, err := io.ReadFull(r, payload[4:]); err != nil {
			return
		}

		seq++
		ts += talkFrameSamples

		c.talkMu.Lock()
		session := c.audioSession
		c.talkMu.Unlock()

		// before the Start and after the Stop
		if session == nil || session.Remote == nil {
			continue
		}

		packet := &rtp.Packet{
			Header:  rtp.Header{Marker: true, SequenceNumber: seq, Timestamp: ts},
			Payload: payload,
		}
		if n, err := session.WriteRTP(packet); err == nil {
			c.Send += n
		}
	}
}

func (c *Client) stopTalk() {
	c.talkMu.Lock()
	if c.talkCmd != nil {
		_ = c.talkCmd.Close()
		c.talkCmd = nil
	}
	c.talkMu.Unlock()
}
