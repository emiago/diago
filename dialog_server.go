package diago

import (
	"context"
	"fmt"
	"net"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
)

// DialogServerSession represents inbound channel
type DialogServerSession struct {
	*sipgo.DialogServerSession
	DialogMedia
}

func (d *DialogServerSession) Close() {
	if d.Session != nil {
		d.Session.Close()
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
	sess, err := sipgox.NewMediaSession(laddr)
	if err != nil {
		return err
	}

	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.Session = sess
	d.RTPReader = sipgox.NewRTPReader(sess)
	d.RTPWriter = sipgox.NewRTPWriter(sess)
	return d.RespondSDP(sess.LocalSDP())
}

func (d *DialogServerSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}
