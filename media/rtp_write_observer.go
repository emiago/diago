package media

import "github.com/pion/rtp"

// rtpWriteObserver boxes the callback so it can live in an atomic.Pointer: a
// func value is not directly storable there, and the box keeps the read on the
// write path to a single atomic load.
type rtpWriteObserver struct {
	fn func(p *rtp.Packet)
}

// OnWriteRTP registers f to be called with every RTP packet this session sends,
// from inside WriteRTP. Pass nil to remove it.
//
// It is the RTP counterpart of RTPSession.OnReadRTCP / OnWriteRTCP: a place to
// observe what actually goes on the wire without wrapping the writer.
//
// Why it cannot be done from outside: RTPPacketWriter records the last header it
// wrote in PacketHeader, but writes it under its own lock and exposes no
// synchronised getter, so reading it after a Write races with anything else
// sending on the same session — DTMF in particular — and can return the header
// of a different packet. WriteRTP is the one place every outgoing packet passes
// through, audio and DTMF alike, and it has the packet in hand.
//
// f is called with the packet BEFORE it is marshalled and, on a secure session,
// before it is encrypted — so an observer sees the media in the clear, which is
// the point for anything that wants to record or analyse what this endpoint
// sent. It runs ON the media path: it must be quick and must not block. The
// packet belongs to the caller and is reused, so anything kept beyond the call
// has to be copied.
//
// Reading the observer costs one atomic load per packet when none is set.
func (m *MediaSession) OnWriteRTP(f func(p *rtp.Packet)) {
	if f == nil {
		m.onWriteRTP.Store(nil)
		return
	}
	m.onWriteRTP.Store(&rtpWriteObserver{fn: f})
}
