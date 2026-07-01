package mediawebrtc

import (
	"testing"

	"github.com/emiago/diago/media"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

func TestMediaNegotiaton(t *testing.T) {
	ctx := t.Context()
	// 	t.Run("UnsupportedRTPProfile", func(t *testing.T) {
	// 		sd := `v=0
	// o=- 3948988145 3948988145 IN IP4 192.168.178.54
	// s=Sip Go Media
	// c=IN IP4 192.168.178.54
	// t=0 0
	// m=audio 34391 RTP/UNKNOWN 0 8
	// a=sendrecv`

	// 		m := MediaSession{
	// 			Codecs: []media.Codec{
	// 				media.CodecAudioAlaw, media.CodecAudioUlaw, media.CodecAudioOpus, media.CodecTelephoneEvent8000,
	// 			},
	// 			// Laddr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
	// 			Mode: "sendrecv",
	// 		}
	// 		require.NoError(t, m.Init(webrtc.Configuration{}))

	// 		err := m.RemoteSDP(ctx, []byte(sd), false)
	// 		require.Error(t, err)
	// 		require.Contains(t, err.Error(), "unsupported media description protocol")
	// 	})

	t.Run("answererPrefersRemoteOfferOrder", func(t *testing.T) {
		sd := `v=0
o=- 3948988145 3948988145 IN IP4 192.168.178.54
s=Sip Go Media
c=IN IP4 192.168.178.54
t=0 0
m=audio 34391 RTP/AVP 0 8
a=sendrecv
`
		sd = `v=0
o=- 123456 1 IN IP4 0.0.0.0
s=-
t=0 0
a=group:BUNDLE 0
a=msid-semantic:WMS *

m=audio 9 UDP/TLS/RTP/SAVPF 0 8 111
c=IN IP4 0.0.0.0

a=mid:0
a=sendrecv
a=rtcp-mux

a=ice-ufrag:test
a=ice-pwd:testpassword1234567890

a=fingerprint:sha-256 AA:BB:CC:DD:EE:FF:...

a=setup:actpass

a=rtpmap:111 opus/48000/2
`

		m := MediaSession{
			Codecs: []media.Codec{
				media.CodecAudioAlaw, media.CodecAudioUlaw, media.CodecAudioOpus, media.CodecTelephoneEvent8000,
			},
			// Laddr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
			Mode: "sendrecv",
		}
		require.NoError(t, m.Init(webrtc.Configuration{}))
		require.Empty(t, m.CommonCodecs())
		err := m.RemoteSDP(ctx, []byte(sd), false)
		require.NoError(t, err)
		require.Equal(t, []media.Codec{
			media.CodecAudioUlaw,
			media.CodecAudioAlaw,
		}, m.CommonCodecs())
		require.Equal(t, media.CodecAudioUlaw, m.Codec())
	})

	t.Run("offererPrefersRemoteAnswerOrder", func(t *testing.T) {
		offerer := MediaSession{
			Codecs: []media.Codec{
				media.CodecAudioAlaw,
				media.CodecAudioUlaw,
				media.CodecAudioOpus,
				media.CodecTelephoneEvent8000,
			},
			Mode: "sendrecv",
		}
		require.NoError(t, offerer.Init(webrtc.Configuration{}))
		defer offerer.Close()

		offer, err := offerer.LocalSDP(ctx, false)
		require.NoError(t, err)
		require.Empty(t, offerer.CommonCodecs())

		answerer := MediaSession{
			Codecs: []media.Codec{
				media.CodecAudioUlaw,
				media.CodecAudioAlaw,
			},
			Mode: "sendrecv",
		}
		require.NoError(t, answerer.Init(webrtc.Configuration{}))
		defer answerer.Close()

		require.NoError(t, answerer.RemoteSDP(ctx, offer, false))
		answer, err := answerer.LocalSDP(ctx, true)
		require.NoError(t, err)
		require.Equal(t, []media.Codec{
			media.CodecAudioUlaw,
			media.CodecAudioAlaw,
		}, answerer.CommonCodecs())
		require.Equal(t, media.CodecAudioUlaw, answerer.Codec())

		require.NoError(t, offerer.RemoteSDP(ctx, answer, true))
		require.Equal(t, []media.Codec{
			media.CodecAudioUlaw,
			media.CodecAudioAlaw,
		}, offerer.CommonCodecs())
		require.Equal(t, media.CodecAudioUlaw, offerer.Codec())
	})

	// 	t.Run("SecuredWithoutCrypto", func(t *testing.T) {
	// 		sd := `v=0
	// o=- 3948988145 3948988145 IN IP4 192.168.178.54
	// s=Sip Go Media
	// c=IN IP4 192.168.178.54
	// t=0 0
	// m=audio 34391 RTP/SAVP 0 8
	// a=sendrecv`

	// 		m := MediaSession{
	// 			Codecs: []Codec{
	// 				CodecAudioAlaw, CodecAudioUlaw, CodecAudioOpus, CodecTelephoneEvent8000,
	// 			},
	// 			Laddr:     net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
	// 			Mode:      "sendrecv",
	// 			SecureRTP: 1,
	// 			SRTPAlg:   SRTPProfileAes128CmHmacSha1_80,
	// 		}
	// 		err := m.RemoteSDP([]byte(sd))
	// 		require.Error(t, err)
	// 		require.Contains(t, err.Error(), "remote requested secure RTP, but no context is created")
	// 	})

	// 	t.Run("Secured", func(t *testing.T) {
	// 		sd := `v=0
	// o=- 3948988145 3948988145 IN IP4 192.168.178.54
	// s=Sip Go Media
	// c=IN IP4 192.168.178.54
	// t=0 0
	// m=audio 34391 RTP/SAVP 0 8
	// a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:8Dlz/SyzlAKCZwH49w5DX8S4pDa7Lw0n3LTI4t6Z
	// a=sendrecv`

	// 		m := MediaSession{
	// 			Codecs: []Codec{
	// 				CodecAudioAlaw, CodecAudioUlaw, CodecAudioOpus, CodecTelephoneEvent8000,
	// 			},
	// 			Laddr:     net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
	// 			Mode:      "sendrecv",
	// 			SecureRTP: 1,
	// 			SRTPAlg:   SRTPProfileAes128CmHmacSha1_80,
	// 		}
	// 		err := m.RemoteSDP([]byte(sd))
	// 		require.NoError(t, err)
	// 	})

}
