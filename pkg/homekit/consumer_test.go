package homekit

import (
	"net"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/hap/camera"
	"github.com/AlexxIT/go2rtc/pkg/srtp"
	"github.com/stretchr/testify/require"
)

type tcpConn struct {
	net.Conn
}

func (tcpConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51826}
}

func (tcpConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 50000}
}

func TestConsumerCryptoSuite(t *testing.T) {
	for _, test := range []struct {
		suite   byte
		keySize byte
	}{
		{0, 16}, // AES_CM_128_HMAC_SHA1_80
		{1, 32}, // AES_256_CM_HMAC_SHA1_80
	} {
		c := NewConsumer(tcpConn{}, srtp.NewServer(":8443"))
		c.SetOffer(&camera.SetupEndpointsRequest{
			VideoCrypto: camera.SRTPCryptoSuite{
				CryptoSuite: test.suite,
				MasterKey:   core.RandString(test.keySize, 0),
				MasterSalt:  core.RandString(14, 0),
			},
			AudioCrypto: camera.SRTPCryptoSuite{
				CryptoSuite: test.suite,
				MasterKey:   core.RandString(test.keySize, 0),
				MasterSalt:  core.RandString(14, 0),
			},
		})

		answer := c.GetAnswer()
		require.Equal(t, test.suite, answer.VideoCrypto.CryptoSuite)
		require.Equal(t, test.suite, answer.AudioCrypto.CryptoSuite)
		require.Len(t, answer.VideoCrypto.MasterKey, int(test.keySize))
		require.Len(t, answer.AudioCrypto.MasterKey, int(test.keySize))
	}
}
