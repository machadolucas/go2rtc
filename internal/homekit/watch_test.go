package homekit

import (
	"testing"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/stretchr/testify/require"
)

func TestWatcherMotion(t *testing.T) {
	const stream = "watcher_motion_test"

	motion := &CharInfo{AID: 1, IID: 10, Type: TypeMotionDetected, Service: "85", Value: false}
	light := &CharInfo{AID: 1, IID: 20, Type: "25", Service: "43", Value: false}
	w := &watcher{
		stream: stream,
		chars:  []*CharInfo{motion, light},
		index:  map[[2]uint64]*CharInfo{{1, 10}: motion, {1, 20}: light},
	}

	_, ok := streams.GetMotion(stream)
	require.False(t, ok)

	// other characteristics don't touch the motion state
	w.update(1, 20, true, true)
	_, ok = streams.GetMotion(stream)
	require.False(t, ok)

	// accessories report bool as true/false or 1/0
	w.update(1, 10, float64(1), true)
	state, ok := streams.GetMotion(stream)
	require.True(t, ok)
	require.True(t, state)

	// values read without notification (characteristics API) also count
	w.update(1, 10, false, false)
	state, _ = streams.GetMotion(stream)
	require.False(t, state)
}
