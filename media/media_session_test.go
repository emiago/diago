// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"io"
	"net"
	"testing"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/fakes"
	"github.com/pion/rtcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaPortRange(t *testing.T) {
	RTPPortStart = 5000
	RTPPortEnd = 5010

	sessions := []*MediaSession{}
	for i := RTPPortStart; i < RTPPortEnd; i += 2 {
		require.Equal(t, i-RTPPortStart, int(rtpPortOffset.Load()))
		mess, err := NewMediaSession(net.IPv4(127, 0, 0, 1), 0)
		t.Log(mess.rtpConn.LocalAddr(), mess.rtcpConn.LocalAddr())
		require.NoError(t, err)
		sessions = append(sessions, mess)
	}

	for _, s := range sessions {
		s.Close()
	}

}

func TestDTMFEncodeDecode(t *testing.T) {
	// Example payload for DTMF digit '1' with volume 10 and duration 1000
	// Event: 0x01 (DTMF digit '1')
	// E bit: 0x80 (End of Event)
	// Volume: 0x0A (Volume 10)
	// Duration: 0x03E8 (Duration 1000)
	payload := []byte{0x01, 0x8A, 0x03, 0xE8}

	event := DTMFEvent{}
	err := DTMFDecode(payload, &event)
	if err != nil {
		t.Fatalf("Error decoding payload: %v", err)
	}

	if event.Event != 0x01 {
		t.Errorf("Unexpected Event. got: %v, want: %v", event.Event, 0x01)
	}

	if event.EndOfEvent != true {
		t.Errorf("Unexpected EndOfEvent. got: %v, want: %v", event.EndOfEvent, true)
	}

	if event.Volume != 0x0A {
		t.Errorf("Unexpected Volume. got: %v, want: %v", event.Volume, 0x0A)
	}

	if event.Duration != 0x03E8 {
		t.Errorf("Unexpected Duration. got: %v, want: %v", event.Duration, 0x03E8)
	}

	encoded := DTMFEncode(event)
	require.Equal(t, payload, encoded)
}

func TestReadRTCP(t *testing.T) {
	session := &MediaSession{}
	reader, writer := io.Pipe()
	session.rtcpConn = &fakes.UDPConn{
		Reader: reader,
	}

	go func() {
		pkts := []rtcp.Packet{
			&rtcp.SenderReport{},
			&rtcp.ReceiverReport{},
		}
		data, err := rtcp.Marshal(pkts)
		if err != nil {
			return
		}
		writer.Write(data)
	}()
	pkts := make([]rtcp.Packet, 5)
	n, err := session.ReadRTCP(make([]byte, 1600), pkts)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.IsType(t, &rtcp.SenderReport{}, pkts[0])
	require.IsType(t, &rtcp.ReceiverReport{}, pkts[1])

}

func TestMediaSessionExternalIP(t *testing.T) {
	m := &MediaSession{
		Formats: sdp.Formats{
			sdp.FORMAT_TYPE_ULAW, sdp.FORMAT_TYPE_ALAW, sdp.FORMAT_TYPE_TELEPHONE_EVENT,
		},
		Laddr:      net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)},
		Mode:       sdp.ModeSendrecv,
		ExternalIP: net.IPv4(1, 1, 1, 1),
	}

	data := m.LocalSDP()
	sd := sdp.SessionDescription{}
	err := sdp.Unmarshal(data, &sd)
	require.NoError(t, err)

	connInfo, err := sd.ConnectionInformation()
	require.NoError(t, err)
	assert.NotEmpty(t, connInfo.IP.To4())
	assert.Equal(t, m.ExternalIP.To4(), connInfo.IP.To4())
}

func TestMediaSessionUpdateCodec(t *testing.T) {

	newM := func() *MediaSession {
		return &MediaSession{
			Formats: sdp.Formats{
				sdp.FORMAT_TYPE_ULAW, sdp.FORMAT_TYPE_ALAW, sdp.FORMAT_TYPE_TELEPHONE_EVENT,
			},
		}
	}

	m := newM()
	m.updateRemoteFormats(sdp.Formats{sdp.FORMAT_TYPE_ALAW, sdp.FORMAT_TYPE_ULAW})
	assert.Equal(t, sdp.Formats{sdp.FORMAT_TYPE_ALAW, sdp.FORMAT_TYPE_ULAW}, m.Formats)

	m = newM()
	m.updateRemoteFormats(sdp.Formats{sdp.FORMAT_TYPE_ALAW})
	assert.Equal(t, sdp.Formats{sdp.FORMAT_TYPE_ALAW}, m.Formats)

	m = newM()
	m.updateRemoteFormats(sdp.Formats{})
	assert.Equal(t, sdp.Formats{}, m.Formats)

	m = newM()
	m.updateRemoteFormats(sdp.Formats{"NonExisting"})
	assert.Equal(t, sdp.Formats{}, m.Formats)
}
