package homekit

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/hap/camera"
	"github.com/AlexxIT/go2rtc/pkg/opus"
	"github.com/AlexxIT/go2rtc/pkg/srtp"
	"github.com/pion/rtp"
)

type Consumer struct {
	core.Connection
	conn net.Conn
	srtp *srtp.Server

	deadline *time.Timer

	sessionID    string
	videoSession *srtp.Session
	audioSession *srtp.Session
	audioRTPTime byte

	videoCrypto byte
	audioCrypto byte
}

func NewConsumer(conn net.Conn, server *srtp.Server) *Consumer {
	medias := []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionSendonly,
			Codecs: []*core.Codec{
				{Name: core.CodecH264},
			},
		},
		{
			Kind:      core.KindAudio,
			Direction: core.DirectionSendonly,
			Codecs: []*core.Codec{
				{Name: core.CodecOpus},
			},
		},
	}
	return &Consumer{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "homekit",
			Protocol:   "rtp",
			RemoteAddr: conn.RemoteAddr().String(),
			Medias:     medias,
			Transport:  conn,
		},
		conn: conn,
		srtp: server,
	}
}

func (c *Consumer) SessionID() string {
	return c.sessionID
}

func (c *Consumer) SetOffer(offer *camera.SetupEndpointsRequest) {
	c.sessionID = offer.SessionID
	// the answer should have the same crypto suite: AES_CM_128 or AES_256_CM
	c.videoCrypto = offer.VideoCrypto.CryptoSuite
	c.audioCrypto = offer.AudioCrypto.CryptoSuite
	c.videoSession = &srtp.Session{
		Remote: &srtp.Endpoint{
			Addr:       offer.Address.IPAddr,
			Port:       offer.Address.VideoRTPPort,
			MasterKey:  []byte(offer.VideoCrypto.MasterKey),
			MasterSalt: []byte(offer.VideoCrypto.MasterSalt),
		},
	}
	c.audioSession = &srtp.Session{
		Remote: &srtp.Endpoint{
			Addr:       offer.Address.IPAddr,
			Port:       offer.Address.AudioRTPPort,
			MasterKey:  []byte(offer.AudioCrypto.MasterKey),
			MasterSalt: []byte(offer.AudioCrypto.MasterSalt),
		},
	}
}

func (c *Consumer) GetAnswer() *camera.SetupEndpointsResponse {
	c.videoSession.Local = c.srtpEndpoint(len(c.videoSession.Remote.MasterKey))
	c.audioSession.Local = c.srtpEndpoint(len(c.audioSession.Remote.MasterKey))

	return &camera.SetupEndpointsResponse{
		SessionID: c.sessionID,
		Status:    camera.StreamingStatusAvailable,
		Address: camera.Address{
			IPAddr:       c.videoSession.Local.Addr,
			VideoRTPPort: c.videoSession.Local.Port,
			AudioRTPPort: c.audioSession.Local.Port,
		},
		VideoCrypto: camera.SRTPCryptoSuite{
			CryptoSuite: c.videoCrypto,
			MasterKey:   string(c.videoSession.Local.MasterKey),
			MasterSalt:  string(c.videoSession.Local.MasterSalt),
		},
		AudioCrypto: camera.SRTPCryptoSuite{
			CryptoSuite: c.audioCrypto,
			MasterKey:   string(c.audioSession.Local.MasterKey),
			MasterSalt:  string(c.audioSession.Local.MasterSalt),
		},
		VideoSSRC: c.videoSession.Local.SSRC,
		AudioSSRC: c.audioSession.Local.SSRC,
	}
}

func (c *Consumer) SetConfig(conf *camera.SelectedStreamConfiguration) bool {
	if c.sessionID != conf.Control.SessionID {
		return false
	}

	c.SDP = fmt.Sprintf("%+v\n%+v", conf.VideoCodec, conf.AudioCodec)

	c.videoSession.Remote.SSRC = conf.VideoCodec.RTPParams[0].SSRC
	c.videoSession.PayloadType = conf.VideoCodec.RTPParams[0].PayloadType
	c.videoSession.RTCPInterval = toDuration(conf.VideoCodec.RTPParams[0].RTCPInterval)

	c.audioSession.Remote.SSRC = conf.AudioCodec.RTPParams[0].SSRC
	c.audioSession.PayloadType = conf.AudioCodec.RTPParams[0].PayloadType
	c.audioSession.RTCPInterval = toDuration(conf.AudioCodec.RTPParams[0].RTCPInterval)
	if len(conf.AudioCodec.CodecParams) > 0 && len(conf.AudioCodec.CodecParams[0].RTPTime) > 0 {
		c.audioRTPTime = conf.AudioCodec.CodecParams[0].RTPTime[0]
	}

	// The controller selects the audio codec from the list of the accessory.
	// For the basic go2rtc camera it is always Opus, but for the proxy with
	// rebroadcast it is the codec of the real camera (usually AAC-ELD).
	if int(conf.AudioCodec.CodecType) < len(audioCodecs) {
		if name := audioCodecs[conf.AudioCodec.CodecType]; name != core.CodecOpus {
			c.Medias[1].Codecs = []*core.Codec{{Name: name}}

			// Two-way audio: the controller sends the same codec back. It can
			// be passed to a producer that supports it as is (HomeKit camera).
			if name == core.CodecELD && len(c.Medias) == 2 {
				c.Medias = append(c.Medias, &core.Media{
					Kind:      core.KindAudio,
					Direction: core.DirectionRecvonly,
					Codecs:    []*core.Codec{{Name: core.CodecELD, ClockRate: 16000}},
				})
			}
		}
	}

	c.srtp.AddSession(c.videoSession)
	c.srtp.AddSession(c.audioSession)

	return true
}

func (c *Consumer) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	var session *srtp.Session
	if codec.Kind() == core.KindVideo {
		session = c.videoSession
	} else {
		session = c.audioSession
	}

	sender := core.NewSender(media, track.Codec)

	if c.deadline == nil {
		c.deadline = time.NewTimer(time.Second * 30)

		sender.Handler = func(packet *rtp.Packet) {
			c.deadline.Reset(core.ConnDeadline)
			if n, err := session.WriteRTP(packet); err == nil {
				c.Send += n
			}
		}
	} else {
		sender.Handler = func(packet *rtp.Packet) {
			if n, err := session.WriteRTP(packet); err == nil {
				c.Send += n
			}
		}
	}

	switch codec.Name {
	case core.CodecH264:
		sender.Handler = h264.RTPPay(1378, sender.Handler)
		if track.Codec.IsRTP() {
			sender.Handler = h264.RTPDepay(track.Codec, sender.Handler)
		} else {
			sender.Handler = h264.RepairAVCC(track.Codec, sender.Handler)
		}
	case core.CodecOpus:
		sender.Handler = opus.RepackToHAP(c.audioRTPTime, sender.Handler)
	}

	sender.HandleRTP(track)
	c.Senders = append(c.Senders, sender)
	return nil
}

// GetTrack - audio from the controller (two-way audio)
func (c *Consumer) GetTrack(media *core.Media, codec *core.Codec) (*core.Receiver, error) {
	if media.Direction != core.DirectionRecvonly || c.audioSession == nil {
		return nil, core.ErrCantGetTrack
	}

	receiver := core.NewReceiver(media, codec)
	c.audioSession.OnReadRTP = func(packet *rtp.Packet) {
		c.Recv += len(packet.Payload)
		receiver.WriteRTP(packet)
	}
	c.Receivers = append(c.Receivers, receiver)
	return receiver, nil
}

// Start - only for the core.Producer interface (two-way audio)
func (c *Consumer) Start() error {
	return nil
}

func (c *Consumer) WriteTo(io.Writer) (int64, error) {
	if c.deadline != nil {
		<-c.deadline.C
	}
	return 0, nil
}

func (c *Consumer) Stop() error {
	if c.deadline != nil {
		c.deadline.Reset(0)
	}
	return c.Connection.Stop()
}

// localIP returns the address the controller should use for the media.
// Usually it is the local address of the HAP connection. But a host can have
// several networks and the controller can reach the HAP port via a router
// (ex. via mDNS reflector), while the media goes directly. So the address from
// the same network as the controller media address is preferred.
func (c *Consumer) localIP() string {
	if c.videoSession != nil && c.videoSession.Remote != nil {
		if remote := net.ParseIP(c.videoSession.Remote.Addr); remote != nil {
			if addrs, err := net.InterfaceAddrs(); err == nil {
				for _, addr := range addrs {
					if ipnet, ok := addr.(*net.IPNet); ok && ipnet.Contains(remote) {
						if ip := ipnet.IP.To4(); ip != nil && !ip.IsLoopback() {
							return ip.String()
						}
					}
				}
			}
		}
	}

	addr := c.conn.LocalAddr().(*net.TCPAddr)
	return addr.IP.To4().String()
}

func (c *Consumer) srtpEndpoint(keySize int) *srtp.Endpoint {
	if keySize != 32 {
		keySize = 16
	}
	return &srtp.Endpoint{
		Addr:       c.localIP(),
		Port:       uint16(c.srtp.Port()),
		MasterKey:  []byte(core.RandString(byte(keySize), 0)),
		MasterSalt: []byte(core.RandString(14, 0)),
		SSRC:       rand.Uint32(),
	}
}

func toDuration(seconds float32) time.Duration {
	return time.Duration(seconds * float32(time.Second))
}
