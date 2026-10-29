// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type updateTestDialog struct {
	state   sip.DialogState
	readErr error
}

func (d *updateTestDialog) LoadState() sip.DialogState { return d.state }
func (d *updateTestDialog) ReadRequest(*sip.Request, sip.ServerTransaction) error {
	return d.readErr
}
func (d *updateTestDialog) Context() context.Context { return context.Background() }

type updateTestMedia struct {
	pending   bool
	remoteErr error
	finalized bool
}

func (m *updateTestMedia) onRemoteSDP(context.Context, []byte, bool) error { return m.remoteErr }
func (m *updateTestMedia) onLocalSDP(context.Context, bool, string) ([]byte, error) {
	return []byte("answer"), nil
}
func (m *updateTestMedia) onFinalize(context.Context) error { m.finalized = true; return nil }
func (m *updateTestMedia) offerPending() bool               { return m.pending }

// respondTx records the response; the handler under test calls nothing else on it
type respondTx struct {
	sip.ServerTransaction
	res *sip.Response
}

func (tx *respondTx) Respond(res *sip.Response) error {
	tx.res = res
	return nil
}

func TestDialogHandleUpdate(t *testing.T) {
	ours := &sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "us", Host: "127.0.0.1"}}
	peers := sip.Uri{Scheme: "sip", User: "peer", Host: "192.0.2.1", Port: 5070}
	newUpdate := func(body string) *sip.Request {
		req := sip.NewRequest(sip.UPDATE, sip.Uri{Scheme: "sip", User: "us", Host: "127.0.0.1"})
		req.AppendHeader(sip.NewHeader("Via", "SIP/2.0/UDP 192.0.2.1:5070;branch=z9hG4bK.update"))
		req.AppendHeader(sip.NewHeader("From", "<sip:peer@192.0.2.1>;tag=peer"))
		req.AppendHeader(sip.NewHeader("To", "<sip:us@127.0.0.1>;tag=us"))
		req.AppendHeader(sip.NewHeader("Call-ID", "update-test"))
		req.AppendHeader(sip.NewHeader("CSeq", "3 UPDATE"))
		req.AppendHeader(&sip.ContactHeader{Address: peers})
		if body != "" {
			req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			req.SetBody([]byte(body))
		}
		return req
	}

	for _, tc := range []struct {
		name    string
		state   sip.DialogState
		readErr error
		body    string
		media   *updateTestMedia
		code    int
	}{
		{name: "BeforeACK", state: sip.DialogStateEstablished, body: "offer", media: &updateTestMedia{}, code: sip.StatusInternalServerError},
		{name: "StaleCSeq", state: sip.DialogStateConfirmed, readErr: sipgo.ErrDialogInvalidCseq, body: "offer", media: &updateTestMedia{}, code: sip.StatusInternalServerError},
		{name: "BadRequest", state: sip.DialogStateConfirmed, readErr: errors.New("broken"), body: "offer", media: &updateTestMedia{}, code: sip.StatusBadRequest},
		{name: "SessionRefresh", state: sip.DialogStateConfirmed, media: &updateTestMedia{}, code: sip.StatusOK},
		{name: "Offer", state: sip.DialogStateConfirmed, body: "offer", media: &updateTestMedia{}, code: sip.StatusOK},
		{name: "OurOfferPending", state: sip.DialogStateConfirmed, body: "offer", media: &updateTestMedia{pending: true}, code: sip.StatusRequestPending},
		{name: "UnacceptableOffer", state: sip.DialogStateConfirmed, body: "offer", media: &updateTestMedia{remoteErr: errors.New("no codec")}, code: sip.StatusNotAcceptableHere},
		{name: "NoMedia", state: sip.DialogStateConfirmed, body: "offer", code: sip.StatusNotAcceptableHere},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := &dialogCallbacks{}
			if tc.media != nil {
				cb.mediaHanshaker = tc.media
			}
			tx := &respondTx{}
			err := dialogHandleUpdate(&updateTestDialog{state: tc.state, readErr: tc.readErr}, cb, newUpdate(tc.body), tx, ours)
			if tc.code == sip.StatusNotAcceptableHere && tc.media != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, tx.res)
			assert.Equal(t, tc.code, tx.res.StatusCode)

			switch tc.code {
			case sip.StatusInternalServerError:
				retry := tx.res.GetHeader("Retry-After")
				require.NotNil(t, retry)
				secs, err := strconv.Atoi(retry.Value())
				require.NoError(t, err)
				assert.True(t, secs >= 0 && secs <= 10)
			case sip.StatusOK:
				require.NotNil(t, tx.res.Contact())
				assert.Equal(t, "us", tx.res.Contact().Address.User)
				assert.Equal(t, peers.User, cb.remoteContact(nil).Address.User, "the UPDATE's Contact is the new remote target")
				if tc.body == "" {
					assert.Empty(t, tx.res.Body())
					assert.False(t, tc.media.finalized)
				} else {
					assert.Equal(t, "answer", string(tx.res.Body()))
					assert.Equal(t, "application/sdp", tx.res.ContentType().Value())
					assert.True(t, tc.media.finalized, "no ACK follows, so the answer is final at once")
				}
			}
		})
	}
}

func TestDiagoAllowsUpdate(t *testing.T) {
	ua, _ := sipgo.NewUA()
	defer ua.Close()
	dg := NewDiago(ua)
	assert.True(t, slices.Contains(dg.server.RegisteredMethods(), "UPDATE"), "OPTIONS answers with the registered methods")
}

// updateRequest is an in-dialog UPDATE toward contact, offering offer when not nil
func updateRequest(contact *sip.ContactHeader, ourContact *sip.ContactHeader, offer []byte) *sip.Request {
	req := sip.NewRequest(sip.UPDATE, contact.Address)
	req.AppendHeader(ourContact.Clone())
	if offer != nil {
		req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		req.SetBody(offer)
	}
	return req
}

func TestIntegrationDialogServerUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("uas"))
	defer ua.Close()
	uas := NewDiago(ua, WithTransport(Transport{Transport: "udp", BindHost: "127.0.0.1", BindPort: 0}))
	answered := make(chan *DialogServerSession, 1)
	err := uas.ServeBackground(ctx, func(d *DialogServerSession) {
		if _, err := d.Answer(AnswerOptions{}); err != nil {
			t.Error(err)
			return
		}
		answered <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)

	peerUA, _ := sipgo.NewUA(sipgo.WithUserAgent("peer"))
	defer peerUA.Close()
	peer := newDialer(peerUA)
	require.NoError(t, peer.ServeBackground(ctx, func(*DialogServerSession) {}))
	uri := sip.Uri{User: "service", Host: "127.0.0.1", Port: uas.transports[0].BindPort}

	dialog, med, err := peer.Invite(ctx, uri, InviteOptions{})
	require.NoError(t, err)
	defer dialog.Close()
	defer med.Close()
	d := <-answered
	require.Eventually(t, func() bool { return d.LoadState() == sip.DialogStateConfirmed }, time.Second, 10*time.Millisecond)

	// A hold: our answer turns the stream around
	hold := med.MediaSession().Fork()
	hold.Mode = sdp.ModeSendonly
	res, err := dialog.Do(ctx, updateRequest(dialog.RemoteContact(), dialog.InviteRequest.Contact(), hold.LocalSDP()))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	require.NotNil(t, res.Contact())
	require.NotNil(t, res.ContentType())
	assert.Equal(t, "application/sdp", res.ContentType().Value())
	assert.Contains(t, string(res.Body()), "a=recvonly")

	// A session refresh carries no SDP
	res, err = dialog.Do(ctx, updateRequest(dialog.RemoteContact(), dialog.InviteRequest.Contact(), nil))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	assert.Empty(t, res.Body())

	require.NoError(t, dialog.Hangup(ctx))
}

func TestIntegrationDialogClientUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("uas"))
	defer ua.Close()
	uas := NewDiago(ua, WithTransport(Transport{Transport: "udp", BindHost: "127.0.0.1", BindPort: 0}))
	type answer struct {
		dialog *DialogServerSession
		media  *DialogMedia
	}
	answered := make(chan answer, 1)
	err := uas.ServeBackground(ctx, func(d *DialogServerSession) {
		med, err := d.Answer(AnswerOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		answered <- answer{d, med}
		<-d.Context().Done()
	})
	require.NoError(t, err)

	// The caller listens: the UPDATE comes to its Contact
	peerUA, _ := sipgo.NewUA(sipgo.WithUserAgent("peer"))
	defer peerUA.Close()
	peer := newDialer(peerUA)
	require.NoError(t, peer.ServeBackground(ctx, func(*DialogServerSession) {}))
	dialog, med, err := peer.Invite(ctx, sip.Uri{User: "service", Host: "127.0.0.1", Port: uas.transports[0].BindPort}, InviteOptions{})
	require.NoError(t, err)
	defer dialog.Close()
	defer med.Close()
	a := <-answered
	require.Eventually(t, func() bool { return a.dialog.LoadState() == sip.DialogStateConfirmed }, time.Second, 10*time.Millisecond)

	hold := a.media.MediaSession().Fork()
	hold.Mode = sdp.ModeSendonly
	res, err := a.dialog.Do(ctx, updateRequest(a.dialog.RemoteContact(), a.dialog.InviteResponse.Contact(), hold.LocalSDP()))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	require.NotNil(t, res.Contact())
	assert.Contains(t, string(res.Body()), "a=recvonly")

	require.NoError(t, dialog.Hangup(ctx))
}
