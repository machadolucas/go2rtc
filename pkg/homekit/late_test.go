package homekit

import (
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/hap/camera"
	"github.com/stretchr/testify/require"
)

func lateClient() *Client {
	return &Client{
		restart:  make(chan struct{}),
		started:  true,
		selVideo: "4D0028",
		selAudio: "ELD/16000",
	}
}

func restarted(c *Client) bool {
	select {
	case <-c.restart:
		return true
	default:
		return false
	}
}

var (
	lateVideo = &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly}
	lateAudio = &core.Media{Kind: core.KindAudio, Direction: core.DirectionRecvonly}
)

func TestSelection(t *testing.T) {
	video := &camera.VideoCodecConfiguration{
		CodecParams: []camera.VideoCodecParameters{{ProfileID: []byte{1}, Level: []byte{2}}},
	}
	require.Equal(t, "4D0028", videoSelection(video))
	require.Equal(t, "", videoSelection(&camera.VideoCodecConfiguration{}))

	audio := &camera.AudioCodecConfiguration{
		CodecType:   2,
		CodecParams: []camera.AudioCodecParameters{{Channels: 1, SampleRate: []byte{1}}},
	}
	require.Equal(t, "ELD/16000", audioSelection(audio))

	require.Equal(t, "4D0028", codecSelection(&core.Codec{Name: core.CodecH264, FmtpLine: "profile-level-id=4D0028"}))
	require.Equal(t, "ELD/16000", codecSelection(&core.Codec{Name: core.CodecELD, ClockRate: 16000}))
}

func TestLateTrackSameCodec(t *testing.T) {
	c := lateClient()

	// video joins the stream started by an audio only consumer
	codec := &core.Codec{Name: core.CodecH264, ClockRate: 90000, FmtpLine: "profile-level-id=4D0028"}
	track, err := c.GetTrack(lateVideo, codec)
	require.NoError(t, err)
	require.Same(t, track, c.videoTrack.Load())
	require.False(t, restarted(c))

	// the same codec again - the same track
	track2, err := c.GetTrack(lateVideo, codec)
	require.NoError(t, err)
	require.Same(t, track, track2)
	require.False(t, restarted(c))

	audio := &core.Codec{Name: core.CodecELD, ClockRate: 16000, Channels: 1}
	track, err = c.GetTrack(lateAudio, audio)
	require.NoError(t, err)
	require.Same(t, track, c.audioTrack.Load())
	require.False(t, restarted(c))
}

func TestLateTrackOtherCodec(t *testing.T) {
	// other audio codec than the camera sends
	c := lateClient()
	_, err := c.GetTrack(lateAudio, &core.Codec{Name: core.CodecOpus, ClockRate: 24000})
	require.NoError(t, err)
	require.Nil(t, c.audioTrack.Load())
	require.True(t, restarted(c))

	// other sample rate
	c = lateClient()
	_, _ = c.GetTrack(lateAudio, &core.Codec{Name: core.CodecELD, ClockRate: 24000})
	require.True(t, restarted(c))

	// other video profile
	c = lateClient()
	_, _ = c.GetTrack(lateVideo, &core.Codec{Name: core.CodecH264, FmtpLine: "profile-level-id=640028"})
	require.Nil(t, c.videoTrack.Load())
	require.True(t, restarted(c))

	// second restart request doesn't panic
	_, _ = c.GetTrack(lateAudio, &core.Codec{Name: core.CodecOpus, ClockRate: 24000})
	require.True(t, restarted(c))
}

func TestTrackBeforeStart(t *testing.T) {
	c := lateClient()
	c.started = false

	_, err := c.GetTrack(lateAudio, &core.Codec{Name: core.CodecOpus, ClockRate: 24000})
	require.NoError(t, err)
	require.Len(t, c.Receivers, 1)
	require.Nil(t, c.audioTrack.Load())
	require.False(t, restarted(c))

	_, err = c.GetTrack(&core.Media{Kind: core.KindAudio, Direction: core.DirectionSendonly}, &core.Codec{Name: core.CodecPCMA})
	require.ErrorIs(t, err, core.ErrCantGetTrack)
}
