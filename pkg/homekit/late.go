package homekit

import (
	"strconv"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/hap/camera"
)

// Tracks requested after the Start.
//
// A HomeKit camera gets the codecs of the stream once, at the start, and then
// always sends both video and audio. A consumer can ask for a track later (ex.
// video after an audio only consumer has started the producer). Such track can
// be attached to the running stream only if its codec is the selected one.
// Otherwise the producer stops, and the streams module starts it again with
// all tracks - the same way as the RTSP producer reconnects.

// videoSelection - H264 profile-level-id of the selected configuration
func videoSelection(conf *camera.VideoCodecConfiguration) string {
	if conf == nil || len(conf.CodecParams) == 0 {
		return ""
	}
	params := conf.CodecParams[0]
	if len(params.ProfileID) == 0 || len(params.Level) == 0 {
		return ""
	}
	profileID, level := int(params.ProfileID[0]), int(params.Level[0])
	if profileID >= len(videoProfiles) || level >= len(videoLevels) {
		return ""
	}
	return videoProfiles[profileID] + videoLevels[level]
}

// audioSelection - name and sample rate of the selected configuration
func audioSelection(conf *camera.AudioCodecConfiguration) string {
	if conf == nil || len(conf.CodecParams) == 0 || int(conf.CodecType) >= len(audioCodecs) {
		return ""
	}
	params := conf.CodecParams[0]
	if len(params.SampleRate) == 0 || int(params.SampleRate[0]) >= len(audioSampleRates) {
		return ""
	}
	return audioCodecs[conf.CodecType] + "/" + strconv.Itoa(int(audioSampleRates[params.SampleRate[0]]))
}

// codecSelection - the same key for the codec of a track
func codecSelection(codec *core.Codec) string {
	switch codec.Kind() {
	case core.KindVideo:
		if codec.Name != core.CodecH264 {
			return ""
		}
		return h264.GetProfileLevelID(codec.FmtpLine)
	case core.KindAudio:
		return codec.Name + "/" + strconv.Itoa(int(codec.ClockRate))
	}
	return ""
}

func (c *Client) GetTrack(media *core.Media, codec *core.Codec) (*core.Receiver, error) {
	if media.Direction == core.DirectionSendonly {
		return nil, core.ErrCantGetTrack
	}

	c.lateMu.Lock()
	defer c.lateMu.Unlock()

	for _, receiver := range c.Receivers {
		if receiver.Codec == codec {
			return receiver, nil
		}
	}

	receiver := core.NewReceiver(media, codec)
	c.Receivers = append(c.Receivers, receiver)

	if c.started {
		c.attachLate(receiver)
	}

	return receiver, nil
}

// attachLate adds the track to the running stream, or asks for the restart
// if the camera sends another codec. Should be called under the lateMu.
func (c *Client) attachLate(receiver *core.Receiver) {
	codec := receiver.Codec
	if codec.Name == core.CodecJPEG {
		return
	}

	key := codecSelection(codec)

	switch codec.Kind() {
	case core.KindVideo:
		if key != "" && key == c.selVideo && c.videoTrack.CompareAndSwap(nil, receiver) {
			return
		}
	case core.KindAudio:
		if key != "" && key == c.selAudio && c.audioTrack.CompareAndSwap(nil, receiver) {
			return
		}
	}

	// Other codec than the selected one (or a second track of the same kind):
	// stop the stream, so it will be started again with this track.
	c.restartOne.Do(func() { close(c.restart) })
}
