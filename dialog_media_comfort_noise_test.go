// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"net"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type comfortNoiseCaptureWriter struct {
	packets []rtp.Packet
}

func (w *comfortNoiseCaptureWriter) WriteRTP(packet *rtp.Packet) error {
	cloned := *packet
	cloned.Payload = bytes.Clone(packet.Payload)
	w.packets = append(w.packets, cloned)
	return nil
}

func TestWithAudioWriterComfortNoise(t *testing.T) {
	rtpWriter := &comfortNoiseCaptureWriter{}
	packetWriter := media.NewRTPPacketWriter(rtpWriter, media.CodecAudioUlaw)
	t.Cleanup(packetWriter.ClockDisable)

	session := &media.MediaSession{
		Codecs: []media.Codec{media.CodecAudioUlaw},
		Laddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
		Mode:   sdp.ModeSendrecv,
	}
	dialog := &DialogMedia{
		mediaSession:    session,
		RTPPacketWriter: packetWriter,
	}

	comfortNoise := &ComfortNoiseWriter{}
	writer, err := dialog.AudioWriter(WithAudioWriterComfortNoise(comfortNoise))
	require.NoError(t, err)
	require.Same(t, comfortNoise, writer)

	require.NoError(t, comfortNoise.WriteComfortNoise(127))
	require.Len(t, rtpWriter.packets, 1)
	require.Equal(t, media.CodecComfortNoise8000.PayloadType, rtpWriter.packets[0].PayloadType)
	require.Equal(t, []byte{127}, rtpWriter.packets[0].Payload)

	localSDP := string(session.LocalSDP())
	require.Contains(t, localSDP, "m=audio 1234 RTP/AVP 0")
	require.NotContains(t, localSDP, "RTP/AVP 0 13")
}

func TestComfortNoiseWriterRequiresPipeline(t *testing.T) {
	w := &ComfortNoiseWriter{}
	require.Error(t, w.WriteComfortNoise(127))
	_, err := w.Write([]byte{0})
	require.Error(t, err)
}
