package diago

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/emiago/media"
	"github.com/emiago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v3"
)

// DialogServerSession represents inbound channel
type DialogServerSession struct {
	*sipgo.DialogServerSession

	// MediaSession *media.MediaSession
	DialogMedia

	webrtPeer *webrtc.PeerConnection

	contactHDR sip.ContactHeader
	formats    sdp.Formats
}

func (d *DialogServerSession) Id() string {
	return d.ID
}

func (d *DialogServerSession) Close() {
	if d.MediaSession != nil {
		d.MediaSession.Close()
	}

	if d.webrtPeer != nil {
		d.webrtPeer.Close()
	}

	d.DialogServerSession.Close()
}

func (d *DialogServerSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

// User that was dialed
func (d *DialogServerSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}

func (d *DialogServerSession) Progress() error {
	return d.Respond(sip.StatusTrying, "Trying", nil)
}

func (d *DialogServerSession) Ringing() error {
	return d.Respond(sip.StatusRinging, "Ringing", nil)
}

func (d *DialogServerSession) DialogSIP() *sipgo.Dialog {
	return &d.Dialog
}

func (d *DialogServerSession) Respond(statusCode sip.StatusCode, reason string, body []byte, headers ...sip.Header) error {
	// TODO fix this on dialog srv
	headers = append(headers, &d.contactHDR)
	return d.DialogServerSession.Respond(statusCode, reason, body, headers...)
}

func (d *DialogServerSession) RespondSDP(body []byte) error {
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	headers = append(headers, &d.contactHDR)
	return d.DialogServerSession.Respond(200, "OK", body, headers...)
}

func (d *DialogServerSession) Answer() error {
	// TODO, lot of here settings need to come from TU. or TU must copy before shipping
	// We may have this settings
	// - Codecs
	// - RTP port ranges

	// For now we keep things global and hardcoded
	// Codecs are ulaw,alaw
	// RTP port range is not set

	// Now media SETUP
	// ip, port, err := sipgox.FindFreeInterfaceHostPort("udp", "")
	// if err != nil {
	// 	return err
	// }

	ip, _, err := sip.ResolveInterfacesIP("ip4", nil)
	if err != nil {
		return err
	}

	laddr := &net.UDPAddr{IP: ip, Port: 0}
	sess, err := media.NewMediaSession(laddr)
	sess.Formats = d.formats
	if err != nil {
		return err
	}

	rtpSess := media.NewRTPSession(sess)
	return d.AnswerWithMedia(rtpSess)
}

func (d *DialogServerSession) AnswerWithMedia(rtpSess *media.RTPSession) error {
	sess := rtpSess.Sess
	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.MediaSession = sess
	rtpSess.MonitorBackground()
	d.RTPPacketReader = media.NewRTPPacketReaderSession(rtpSess)
	d.RTPPacketWriter = media.NewRTPPacketWriterSession(rtpSess)
	if err := d.RespondSDP(sess.LocalSDP()); err != nil {
		return err
	}

	// Wait ACK
	// If we do not wait ACK, hanguping call will fail as ACK can be delayed when we are doing Hangup
	for {
		select {
		case <-time.After(10 * time.Second):
			return fmt.Errorf("no ACK received")
		case state := <-d.State():
			if state == sip.DialogStateConfirmed {
				return nil
			}
		}
	}
}

func (d *DialogServerSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}
