package diago

import (
	"context"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogServerReinvite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15070,
			},
		))

		// Run listener to accepte reinvites, but it should not receive any request
		err := dg.ServeBackground(ctx, nil)
		require.NoError(t, err)

		go func() {
			dialog, err := dg.Invite(ctx, sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15060}, InviteOptions{})
			require.NoError(t, err)
			<-dialog.Context().Done()
			t.Log("Dialog done")
		}()
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15060,
		},
	))

	waitDialog := make(chan *DialogServerSession)
	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		t.Log("Call received")
		waitDialog <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)
	d := <-waitDialog

	err = d.Answer()
	require.NoError(t, err)
	err = d.ReInvite(d.Context())
	require.NoError(t, err)

	d.Hangup(context.TODO())
}

func TestIntegrationDialogServerPlayback(t *testing.T) {
	rtpBuf := newRTPWriterBuffer()
	dialog := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession:    &media.MediaSession{Codecs: []media.Codec{media.CodecAudioUlaw}},
			RTPPacketWriter: media.NewRTPPacketWriter(rtpBuf, media.CodecAudioUlaw),
		},
	}

	playback, err := dialog.PlaybackCreate()
	require.NoError(t, err)

	initTS := dialog.RTPPacketWriter.InitTimestamp()
	_, err = playback.PlayFile("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	diffTS := dialog.RTPPacketWriter.PacketHeader.Timestamp - initTS
	assert.Greater(t, diffTS, uint32(1000))

	time.Sleep(100 * time.Millisecond) // 4 frames
	initTS = dialog.RTPPacketWriter.InitTimestamp()
	_, err = playback.PlayFile("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	diffTS2 := dialog.RTPPacketWriter.PacketHeader.Timestamp - initTS
	t.Log(initTS, diffTS2)

	// Timestamp should be offset more than previous diff by Sleep
	assert.Greater(t, diffTS2, diffTS+5*media.CodecAudioUlaw.SampleTimestamp())
}
