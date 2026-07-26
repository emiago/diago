// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type comfortNoiseRTPWriter struct {
	packets []rtp.Packet
}

func (w *comfortNoiseRTPWriter) WriteRTP(packet *rtp.Packet) error {
	cloned := *packet
	cloned.Payload = bytes.Clone(packet.Payload)
	w.packets = append(w.packets, cloned)
	return nil
}

func TestRTPComfortNoiseWriterLevels(t *testing.T) {
	rtpWriter := &comfortNoiseRTPWriter{}
	packetWriter := NewRTPPacketWriter(rtpWriter, CodecAudioUlaw)
	t.Cleanup(packetWriter.ClockDisable)

	w := NewRTPComfortNoiseWriter(packetWriter, packetWriter)
	for _, level := range []uint8{0, 127} {
		require.NoError(t, w.WriteComfortNoise(level))
		packet := rtpWriter.packets[len(rtpWriter.packets)-1]
		require.Equal(t, CodecComfortNoise8000.PayloadType, packet.PayloadType)
		require.Equal(t, []byte{level}, packet.Payload)
		require.False(t, packet.Marker)
	}

	packetCount := len(rtpWriter.packets)
	require.Error(t, w.WriteComfortNoise(128))
	require.Len(t, rtpWriter.packets, packetCount)

	opusPacketWriter := NewRTPPacketWriter(rtpWriter, CodecAudioOpus)
	t.Cleanup(opusPacketWriter.ClockDisable)
	opusWriter := NewRTPComfortNoiseWriter(opusPacketWriter, opusPacketWriter)
	require.Error(t, opusWriter.WriteComfortNoise(127))
	require.Len(t, rtpWriter.packets, packetCount)
}

func TestRTPComfortNoiseWriterTimeline(t *testing.T) {
	rtpWriter := &comfortNoiseRTPWriter{}
	packetWriter := NewRTPPacketWriter(rtpWriter, CodecAudioUlaw)
	packetWriter.clockTicker.Reset(time.Nanosecond)
	t.Cleanup(packetWriter.ClockDisable)

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	w := NewRTPComfortNoiseWriter(packetWriter, packetWriter)
	w.now = func() time.Time { return now }
	w.lastWriteTime = now

	audioPayload := make([]byte, CodecAudioUlaw.SampleTimestamp())
	_, err := w.Write(audioPayload)
	require.NoError(t, err)

	now = now.Add(15 * time.Second)
	require.NoError(t, w.WriteComfortNoise(127))

	now = now.Add(15 * time.Second)
	require.NoError(t, w.WriteComfortNoise(127))

	now = now.Add(time.Second)
	_, err = w.Write(audioPayload)
	require.NoError(t, err)

	_, err = w.Write(audioPayload)
	require.NoError(t, err)

	require.Len(t, rtpWriter.packets, 5)
	firstAudio := rtpWriter.packets[0]
	firstCN := rtpWriter.packets[1]
	secondCN := rtpWriter.packets[2]
	resumedAudio := rtpWriter.packets[3]
	continuedAudio := rtpWriter.packets[4]

	for i := 1; i < len(rtpWriter.packets); i++ {
		require.Equal(t, firstAudio.SSRC, rtpWriter.packets[i].SSRC)
		require.Equal(t, rtpWriter.packets[i-1].SequenceNumber+1, rtpWriter.packets[i].SequenceNumber)
	}

	require.Equal(t, firstAudio.Timestamp+CodecAudioUlaw.SampleTimestamp()+15*CodecComfortNoise8000.SampleRate, firstCN.Timestamp)
	require.Equal(t, firstCN.Timestamp+15*CodecComfortNoise8000.SampleRate, secondCN.Timestamp)
	require.Equal(t, secondCN.Timestamp+CodecComfortNoise8000.SampleRate, resumedAudio.Timestamp)
	require.True(t, resumedAudio.Marker)
	require.Equal(t, CodecAudioUlaw.PayloadType, resumedAudio.PayloadType)
	require.Equal(t, resumedAudio.Timestamp+CodecAudioUlaw.SampleTimestamp(), continuedAudio.Timestamp)
	require.False(t, continuedAudio.Marker)
}
