package diago

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/mediawebrtc"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialogWebrtcAudioReaderWriterProps(t *testing.T) {
	dialog := &DialogWebrtc{
		mediaSession: &mediawebrtc.MediaSession{
			Codecs: []media.Codec{media.CodecAudioUlaw},
			Laddr:  "local",
			Raddr:  "remote",
		},
	}

	readerProps := MediaProps{}
	_, err := dialog.AudioReader(WithAudioReaderWebrtcProps(&readerProps))
	require.NoError(t, err)
	assert.Equal(t, media.CodecAudioUlaw, readerProps.Codec)
	assert.Equal(t, "local", readerProps.Laddr)
	assert.Equal(t, "remote", readerProps.Raddr)

	writerProps := MediaProps{}
	_, err = dialog.AudioWriter(WithAudioWriterWebrtcProps(&writerProps))
	require.NoError(t, err)
	assert.Equal(t, media.CodecAudioUlaw, writerProps.Codec)
	assert.Equal(t, "local", writerProps.Laddr)
	assert.Equal(t, "remote", writerProps.Raddr)
}

func TestDialogWebrtcServerPlaybackClientReceivesRTP(t *testing.T) {
	require.NoError(t, webrtcInit([]net.IP{net.IPv4(127, 0, 0, 1)}))
	t.Cleanup(func() {
		require.NoError(t, webrtcInit([]net.IP{}))
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverUA, err := sipgo.NewUA()
	require.NoError(t, err)
	defer serverUA.Close()

	server := NewDiago(serverUA,
		WithTransport(Transport{
			Transport: "tcp",
			BindHost:  "127.0.0.1",
			BindPort:  0,
		}),
	)

	playDone := make(chan error, 1)
	err = server.ServeBackground(ctx, func(inDialog *DialogServerSession) {
		err := func() error {
			inDialog.Trying()
			inDialog.Ringing()

			med, err := inDialog.AnswerWebrtc(AnswerWebrtcOptions{})
			if err != nil {
				return err
			}
			defer med.Close()

			playfile, err := testdata.OpenFile("demo-echodone.wav")
			if err != nil {
				return err
			}
			defer playfile.Close()

			pb, err := med.PlaybackCreate()
			if err != nil {
				return err
			}
			_, err = pb.Play(playfile, "audio/wav")
			return err
		}()

		playDone <- err
		if err == nil {
			_ = inDialog.Hangup(context.Background())
		}
	})
	require.NoError(t, err)
	require.NotZero(t, server.transports[0].BindPort)

	clientUA, err := sipgo.NewUA()
	require.NoError(t, err)
	defer clientUA.Close()

	client := NewDiago(clientUA,
		WithTransport(Transport{
			Transport: "tcp",
			BindHost:  "127.0.0.1",
			BindPort:  0,
		}),
	)

	dialog, err := client.NewDialog(sip.Uri{
		User: "playback",
		Host: "127.0.0.1",
		Port: server.transports[0].BindPort,
	}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	inviteCtx, inviteCancel := context.WithTimeout(ctx, 10*time.Second)
	defer inviteCancel()

	med, err := dialog.InviteWebrtc(inviteCtx, InviteWebrtcOptions{})
	require.NoError(t, err)
	defer med.Close()

	// require.Eventually(t, func() bool {
	// 	med.mu.Lock()
	// 	defer med.mu.Unlock()
	// 	return med.mediaSession.RTPPacketReader.Reader() != nil
	// 	return med.mediaSession.reader != nil
	// }, 5*time.Second, 10*time.Millisecond)

	med.mu.Lock()
	require.NoError(t, med.mediaSession.StopRTP(1, 5*time.Second))
	med.mu.Unlock()

	props := MediaProps{}
	audioR, err := med.AudioReader(WithAudioReaderWebrtcProps(&props))
	require.NoError(t, err)
	require.Equal(t, media.CodecAudioUlaw, props.Codec)

	buf := make([]byte, media.RTPBufSize)
	var prevSeq uint16
	var prevTimestamp uint32
	for i := range 20 {
		n, err := audioR.Read(buf)
		require.NoError(t, err)
		require.Equal(t, int(media.CodecAudioUlaw.SampleTimestamp()), n)

		header := med.RTPPacketReader.PacketHeader
		require.Equal(t, uint8(2), header.Version)
		require.Equal(t, media.CodecAudioUlaw.PayloadType, header.PayloadType)
		require.NotZero(t, header.SSRC)
		require.NotEmpty(t, buf[:n])

		if i > 0 {
			require.Equal(t, prevSeq+1, header.SequenceNumber)
			require.Equal(t, prevTimestamp+media.CodecAudioUlaw.SampleTimestamp(), header.Timestamp)
		}
		prevSeq = header.SequenceNumber
		prevTimestamp = header.Timestamp
	}

	require.NoError(t, <-playDone)
}

type capturedRTPPacket struct {
	header  rtp.Header
	payload []byte
}

type recordingRTPWriter struct {
	next    media.RTPWriter
	packets chan capturedRTPPacket
}

func (w *recordingRTPWriter) WriteRTP(p *rtp.Packet) error {
	payload := append([]byte(nil), p.Payload...)
	select {
	case w.packets <- capturedRTPPacket{header: p.Header, payload: payload}:
	default:
	}
	return w.next.WriteRTP(p)
}

func TestDialogWebrtcServerPlaybackPayloadSurvivesWebrtc(t *testing.T) {
	require.NoError(t, webrtcInit([]net.IP{net.IPv4(127, 0, 0, 1)}))
	t.Cleanup(func() {
		require.NoError(t, webrtcInit([]net.IP{}))
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	serverUA, err := sipgo.NewUA()
	require.NoError(t, err)
	defer serverUA.Close()

	sentPackets := make(chan capturedRTPPacket, 256)
	startPlayback := make(chan struct{})
	playDone := make(chan error, 1)

	server := NewDiago(serverUA,
		WithTransport(Transport{
			Transport: "tcp",
			BindHost:  "127.0.0.1",
			BindPort:  0,
		}),
	)

	err = server.ServeBackground(ctx, func(inDialog *DialogServerSession) {
		err := func() error {
			inDialog.Trying()
			inDialog.Ringing()

			med, err := inDialog.AnswerWebrtc(AnswerWebrtcOptions{})
			if err != nil {
				return err
			}
			defer med.Close()

			med.RTPPacketWriter = media.NewRTPPacketWriter(&recordingRTPWriter{
				next:    med.RTPPacketWriter.Writer(),
				packets: sentPackets,
			}, media.CodecAudioUlaw)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-startPlayback:
			}

			playfile, err := testdata.OpenFile("demo-echodone.wav")
			if err != nil {
				return err
			}
			defer playfile.Close()

			pb, err := med.PlaybackCreate()
			if err != nil {
				return err
			}
			_, err = pb.Play(playfile, "audio/wav")
			return err
		}()

		playDone <- err
		if err == nil {
			_ = inDialog.Hangup(context.Background())
		}
	})
	require.NoError(t, err)

	clientUA, err := sipgo.NewUA()
	require.NoError(t, err)
	defer clientUA.Close()

	client := NewDiago(clientUA,
		WithTransport(Transport{
			Transport: "tcp",
			BindHost:  "127.0.0.1",
			BindPort:  0,
		}),
	)

	dialog, err := client.NewDialog(sip.Uri{
		User: "playback",
		Host: "127.0.0.1",
		Port: server.transports[0].BindPort,
	}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	inviteCtx, inviteCancel := context.WithTimeout(ctx, 10*time.Second)
	defer inviteCancel()

	med, err := dialog.InviteWebrtc(inviteCtx, InviteWebrtcOptions{})
	require.NoError(t, err)
	defer med.Close()

	close(startPlayback)

	// require.Eventually(t, func() bool {
	// 	med.mu.Lock()
	// 	defer med.mu.Unlock()
	// 	return med.mediaSession.reader != nil
	// }, 5*time.Second, 10*time.Millisecond)

	med.mu.Lock()
	require.NoError(t, med.mediaSession.StopRTP(1, 5*time.Second))
	med.mu.Unlock()

	audioR, err := med.AudioReader()
	require.NoError(t, err)

	buf := make([]byte, media.RTPBufSize)
	var prevSentSeq uint16
	var prevSentTimestamp uint32
	var prevRecvSeq uint16
	var prevRecvTimestamp uint32
	for i := range 20 {
		var sent capturedRTPPacket
		select {
		case sent = <-sentPackets:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for sent RTP packet %d", i)
		}

		n, err := audioR.Read(buf)
		require.NoError(t, err)

		recvHeader := med.RTPPacketReader.PacketHeader
		require.Equal(t, int(media.CodecAudioUlaw.SampleTimestamp()), len(sent.payload))
		require.Equal(t, int(media.CodecAudioUlaw.SampleTimestamp()), n)
		require.True(t, bytes.Equal(sent.payload, buf[:n]), "payload changed at packet %d", i)

		if i > 0 {
			require.Equal(t, prevSentSeq+1, sent.header.SequenceNumber)
			require.Equal(t, prevSentTimestamp+media.CodecAudioUlaw.SampleTimestamp(), sent.header.Timestamp)
			require.Equal(t, prevRecvSeq+1, recvHeader.SequenceNumber)
			require.Equal(t, prevRecvTimestamp+media.CodecAudioUlaw.SampleTimestamp(), recvHeader.Timestamp)
		}

		prevSentSeq = sent.header.SequenceNumber
		prevSentTimestamp = sent.header.Timestamp
		prevRecvSeq = recvHeader.SequenceNumber
		prevRecvTimestamp = recvHeader.Timestamp
	}

	require.NoError(t, <-playDone)
}
