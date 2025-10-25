// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package ed137

import (
	"bytes"
	"testing"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rtpWriterFunc func(p *rtp.Packet) error

func (f rtpWriterFunc) WriteRTP(p *rtp.Packet) error {
	return f(p)
}

type rtpReaderFunc func(p *rtp.Packet) error

func (f rtpReaderFunc) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	if err := f(p); err != nil {
		return 0, err
	}
	return p.MarshalTo(buf)
}

func TestED137RTPIO(t *testing.T) {
	packetsSent := []*rtp.Packet{}
	rtpWriter := rtpWriterFunc(func(p *rtp.Packet) error {
		packetsSent = append(packetsSent, p.Clone())
		return nil
	})
	rtpPacketWriter := media.NewRTPPacketWriter(rtpWriter, media.CodecAudioAlaw)
	w := NewRTPED137Writer(rtpPacketWriter)

	alaw := make([]byte, 160)
	audio.EncodeAlawTo(alaw, bytes.Repeat([]byte{0, 16, 64, 0}, 80))

	for _, ptt := range []uint8{PTT_OFF, PTT_NORMAL_ON, PTT_OFF} {
		w.PTT(ptt)
		_, err := w.Write(alaw)
		require.NoError(t, err)
	}

	require.Len(t, packetsSent, 3)
	assert.Equal(t, packetsSent[0].GetExtension(0), RTPTxExtension{PTTType: PTT_OFF, PTTID: 1}.Marshal())
	assert.Equal(t, packetsSent[1].GetExtension(0), RTPTxExtension{PTTType: PTT_NORMAL_ON, PTTID: 1}.Marshal())
	assert.Equal(t, packetsSent[2].GetExtension(0), RTPTxExtension{PTTType: PTT_OFF, PTTID: 1}.Marshal())

	i := 0
	rtpReader := rtpReaderFunc(func(p *rtp.Packet) error {
		*p = *packetsSent[i]
		i++
		return nil
	})

	rtpPacketReader := media.NewRTPPacketReader(rtpReader, media.CodecAudioAlaw)
	r := NewRTPED137Reader(rtpPacketReader)
	r.OnPTTChange = func(ptt uint8) error {
		t.Log("PTT change", ptt)
		return nil
	}
	for _, ptt := range []uint8{PTT_OFF, PTT_NORMAL_ON, PTT_OFF} {
		_, err := r.Read(make([]byte, 160))
		require.NoError(t, err)
		actualPtt, err := r.ReadPTT()
		require.NoError(t, err)
		assert.Equal(t, ptt, actualPtt)
	}
}
