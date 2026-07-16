// SPDX-License-Identifier: MPL-2.0

package diago

import (
	"io"
	"net"
	"strconv"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRTPWriter records what actually goes on the wire.
type captureRTPWriter struct {
	pkts []rtp.Packet
}

func (w *captureRTPWriter) WriteRTP(p *rtp.Packet) error {
	w.pkts = append(w.pkts, *p)
	return nil
}

// dtmfNegotiatedSession returns a session that offered telephone-event at our
// default 101 and answered a peer that numbers it tePayloadType.
func dtmfNegotiatedSession(t *testing.T, tePayloadType string) *media.MediaSession {
	t.Helper()
	sess := &media.MediaSession{
		Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecTelephoneEvent8000},
		Laddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
		Mode:   sdp.ModeSendrecv,
	}
	require.NoError(t, sess.Init())
	t.Cleanup(func() { sess.Close() })

	offer := "v=0\r\n" +
		"o=- 3948988145 3948988145 IN IP4 127.0.0.2\r\n" +
		"s=Sip Go Media\r\n" +
		"c=IN IP4 127.0.0.2\r\n" +
		"t=0 0\r\n" +
		"m=audio 34391 RTP/AVP 0 " + tePayloadType + "\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=rtpmap:" + tePayloadType + " telephone-event/8000\r\n" +
		"a=fmtp:" + tePayloadType + " 0-16\r\n" +
		"a=ptime:20\r\n" +
		"a=sendrecv\r\n"
	require.NoError(t, sess.RemoteSDP([]byte(offer)))

	te, ok := media.CodecTelephoneEventFromSession(sess)
	require.True(t, ok, "telephone-event must be negotiated for this fixture")
	require.Equal(t, tePayloadType, strconv.Itoa(int(te.PayloadType)), "fixture must negotiate the peer's number")
	return sess
}

// DTMF goes out on the payload type negotiation settled on. telephone-event is a
// dynamic format, so a peer numbering it 96 gets DTMF at 96: stamping our own
// constant means the peer never recognises the event (RFC 3264 section 6.1).
func TestDTMFWriterUsesNegotiatedPayloadType(t *testing.T) {
	sess := dtmfNegotiatedSession(t, "96")
	capture := &captureRTPWriter{}

	d := &DialogMedia{
		mediaSession:    sess,
		RTPPacketWriter: media.NewRTPPacketWriter(capture, media.CodecAudioUlaw),
	}

	w := &DTMFWriter{}
	require.NoError(t, WithAudioWriterDTMF(w)(d))
	require.NoError(t, w.WriteDTMF('1'))

	require.NotEmpty(t, capture.pkts, "DTMF must be written")
	for _, p := range capture.pkts {
		assert.EqualValues(t, 96, p.PayloadType, "DTMF must carry the negotiated telephone-event number")
	}
}

// The reader gates incoming packets on the same number. A peer sending DTMF at 96
// while we compare against 101 means the event is never decoded as DTMF, and its
// RFC 4733 payload falls through into the audio decoder as if it were speech.
func TestDTMFReaderUsesNegotiatedPayloadType(t *testing.T) {
	sess := dtmfNegotiatedSession(t, "96")

	// An RFC 4733 event for digit 1 as the peer numbers it: a start packet
	// carrying the marker, then the end of event.
	events := []media.DTMFEvent{
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
	}
	pkts := make([]rtp.Packet, 0, len(events))
	for i, ev := range events {
		pkts = append(pkts, rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SSRC:           1234,
				SequenceNumber: uint16(i + 1),
				Timestamp:      160,
				Marker:         i == 0,
			},
			Payload: media.DTMFEncode(ev),
		})
	}

	d := &DialogMedia{
		mediaSession:    sess,
		RTPPacketReader: media.NewRTPPacketReader(&sequenceRTPReader{pkts: pkts}, media.CodecAudioUlaw),
	}

	r := &DTMFReader{}
	require.NoError(t, WithAudioReaderDTMF(r)(d))

	got := make(chan rune, 1)
	r.OnDTMF(func(dtmf rune) error {
		got <- dtmf
		return nil
	})

	buf := make([]byte, media.RTPBufSize)
	for range pkts {
		_, err := r.Read(buf)
		require.NoError(t, err)
	}

	select {
	case dtmf := <-got:
		assert.Equal(t, '1', dtmf)
	default:
		t.Fatal("DTMF at the negotiated telephone-event number was not decoded as DTMF")
	}
}

// sequenceRTPReader replays packets the way the wire delivers them: the whole
// packet lands in buf, readPkt is parsed out of it, and n counts header plus
// payload.
type sequenceRTPReader struct {
	pkts []rtp.Packet
	next int
}

func (r *sequenceRTPReader) ReadRTP(buf []byte, readPkt *rtp.Packet) (int, error) {
	if r.next >= len(r.pkts) {
		return 0, io.EOF
	}
	raw, err := r.pkts[r.next].Marshal()
	if err != nil {
		return 0, err
	}
	r.next++

	n := copy(buf, raw)
	if err := readPkt.Unmarshal(buf[:n]); err != nil {
		return 0, err
	}
	return n, nil
}
