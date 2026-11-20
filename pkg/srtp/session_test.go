package srtp

import (
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

func TestEndpointAES256(t *testing.T) {
	key := []byte(core.RandString(32, 0))
	salt := []byte(core.RandString(14, 0))

	local := &Endpoint{Addr: "127.0.0.1", MasterKey: key, MasterSalt: salt}
	require.Nil(t, local.init())

	remote := &Endpoint{Addr: "127.0.0.1", MasterKey: key, MasterSalt: salt}
	require.Nil(t, remote.init())

	packet := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 99, SequenceNumber: 1, SSRC: 123},
		Payload: []byte{1, 2, 3, 4},
	}
	b, err := packet.Marshal()
	require.Nil(t, err)

	b, err = local.srtp.EncryptRTP(nil, b, nil)
	require.Nil(t, err)

	var header rtp.Header
	b, err = remote.srtp.DecryptRTP(nil, b, &header)
	require.Nil(t, err)

	var packet2 rtp.Packet
	require.Nil(t, packet2.Unmarshal(b))
	require.Equal(t, packet.Payload, packet2.Payload)
}
