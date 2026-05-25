package diago

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

type WebrtcTrackRTPReader struct {
	track    *webrtc.TrackRemote
	receiver *webrtc.RTPReceiver
}

func (r *WebrtcTrackRTPReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	n, _, err := r.track.Read(buf)
	if err != nil {
		return n, err
	}

	err = p.Unmarshal(buf[:n])
	if media.RTPDebug {
		fmt.Fprintf(os.Stderr, "=== Recv RTP ===\n%s", p.String())
	}
	return n, err
}

func (r *WebrtcTrackRTPReader) ReadRTPRaw(buf []byte) (int, error) {
	n, _, err := r.track.Read(buf)
	if media.RTPDebug {
		slog.Debug(fmt.Sprintf("Recv RTP Raw len=%d\n", n))
	}
	return n, err
}

func (r *WebrtcTrackRTPReader) ReadRTCP(buf []byte, rtcpBuf []rtcp.Packet) (int, error) {
	n, _, rtcpErr := r.receiver.Read(buf)
	if rtcpErr != nil {
		return n, rtcpErr
	}

	return media.RTCPUnmarshal(buf[:n], rtcpBuf)
}

func (r *WebrtcTrackRTPReader) ReadRTCPRaw(buf []byte) (int, error) {
	n, _, rtcpErr := r.receiver.Read(buf)
	return n, rtcpErr
}

type WebrtcTrackRTPWriter struct {
	mu      sync.RWMutex
	track   *webrtc.TrackLocalStaticRTP
	sender  *webrtc.RTPSender
	enabled bool
}

func (r *WebrtcTrackRTPWriter) WriteRTP(p *rtp.Packet) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled || r.track == nil {
		return nil
	}
	if media.RTPDebug {
		fmt.Fprintf(os.Stderr, "=== Sent RTP ===\n%s\n", p.String())
	}
	return r.track.WriteRTP(p)
}

func (r *WebrtcTrackRTPWriter) WriteRTPRaw(buf []byte) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled || r.track == nil {
		return len(buf), nil
	}
	if media.RTPDebug {
		slog.Debug(fmt.Sprintf("Recv RTP Raw len=%d\n", len(buf)))
	}
	return r.track.Write(buf)
}

func (r *WebrtcTrackRTPWriter) WriteRTCP(p rtcp.Packet) error {
	// By default pion does RTCP sending by default
	return nil
}

func (r *WebrtcTrackRTPWriter) WriteRTCPRaw(buf []byte) (int, error) {
	// By default pion does RTCP sending by default
	return 0, nil
}

func (writer *WebrtcTrackRTPWriter) ReplaceTrack(track *webrtc.TrackLocalStaticRTP) error {
	writer.mu.RLock()
	defer writer.mu.RUnlock()
	sender := writer.sender
	if err := sender.ReplaceTrack(track); err != nil {
		return err
	}
	writer.track = track
	return nil
}

func (writer *WebrtcTrackRTPWriter) UpdateDirection(shouldSend bool) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	if !shouldSend {
		// In Pion, ReplaceTrack(nil) detaches the local source from the existing
		// RTPSender/transceiver without renegotiating or removing the m-line.
		// The sender stays available so we can attach writer.track again when
		// direction resumes; writer.enabled makes local writes a no-op meanwhile.
		if err := writer.sender.ReplaceTrack(nil); err != nil {
			return err
		}
		writer.enabled = false
		return nil
	}

	if writer.track == nil {
		return fmt.Errorf("webrtc media writer has no local track")
	}
	if err := writer.sender.ReplaceTrack(writer.track); err != nil {
		return err
	}
	writer.enabled = true
	return nil
}

func (r *WebrtcTrackRTPWriter) Track() *webrtc.TrackLocalStaticRTP {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.track
}

func (r *WebrtcTrackRTPWriter) SetEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = enabled
}
