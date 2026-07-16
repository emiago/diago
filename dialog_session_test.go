// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"log/slog"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// referNotifyDialog is a minimal DialogSession. Only Media and Hangup are
// reached by dialogHandleReferNotify, the rest stays unimplemented.
type referNotifyDialog struct {
	DialogSession
	media   *DialogMedia
	hangups int
}

func (d *referNotifyDialog) Media() *DialogMedia { return d.media }

func (d *referNotifyDialog) Hangup(ctx context.Context) error {
	d.hangups++
	return nil
}

func newReferNotifyRequest(t *testing.T, contentType string, body string) *sip.Request {
	t.Helper()

	viaParams := sip.NewParams()
	viaParams.Add("branch", sip.GenerateBranch())
	fromParams := sip.NewParams()
	fromParams.Add("tag", "fromtag")
	toParams := sip.NewParams()
	toParams.Add("tag", "totag")

	req := sip.NewRequest(sip.NOTIFY, sip.Uri{User: "bob", Host: "127.0.0.1", Port: 5060})
	req.AppendHeader(&sip.ViaHeader{
		ProtocolName:    "SIP",
		ProtocolVersion: "2.0",
		Transport:       "UDP",
		Host:            "127.0.0.1",
		Port:            5060,
		Params:          viaParams,
	})
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{User: "alice", Host: "127.0.0.1"},
		Params:  fromParams,
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{User: "bob", Host: "127.0.0.1"},
		Params:  toParams,
	})
	callid := sip.CallIDHeader("refer-notify-test")
	req.AppendHeader(&callid)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.NOTIFY})
	req.AppendHeader(sip.NewHeader("Content-Type", contentType))
	req.SetBody([]byte(body))
	return req
}

func newReferNotifyTx(t *testing.T, req *sip.Request) (*sip.ServerTx, *connRecorder) {
	t.Helper()

	key, err := sip.ServerTxKeyMake(req)
	require.NoError(t, err)

	conn := NewConnRecorder()
	tx := sip.NewServerTx(key, req, conn, slog.Default())
	require.NoError(t, tx.Init())
	return tx, conn
}

// TestDialogHandleReferNotifyContentType checks the Content-Type gate on REFER
// NOTIFY. RFC 3420 Section 5 makes the version parameter of message/sipfrag
// optional and defaulting to "2.0", so a NOTIFY that omits it must still be
// accepted and reach the OnNotify callback.
func TestDialogHandleReferNotifyContentType(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		expectCode  int
		expectFired bool
	}{
		{
			// Regression: 3CX and other PBXes omit the optional version param.
			name:        "sipfrag without version param",
			contentType: "message/sipfrag",
			expectCode:  sip.StatusOK,
			expectFired: true,
		},
		{
			name:        "sipfrag with version param",
			contentType: "message/sipfrag;version=2.0",
			expectCode:  sip.StatusOK,
			expectFired: true,
		},
		{
			name:        "unrelated content type is rejected",
			contentType: "application/sdp",
			expectCode:  sip.StatusBadRequest,
			expectFired: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notified := -1
			d := &referNotifyDialog{media: &DialogMedia{}}
			d.media.onReferNotify = func(statusCode int) { notified = statusCode }

			req := newReferNotifyRequest(t, tc.contentType, "SIP/2.0 200 OK")
			tx, conn := newReferNotifyTx(t, req)

			dialogHandleReferNotify(d, req, tx)

			require.Len(t, conn.msgs, 1)
			res, ok := conn.msgs[0].(*sip.Response)
			require.True(t, ok)
			require.Equal(t, tc.expectCode, res.StatusCode)

			if tc.expectFired {
				require.Equal(t, sip.StatusOK, notified, "OnNotify should receive the sipfrag status code")
			} else {
				require.Equal(t, -1, notified, "OnNotify must not fire on a rejected NOTIFY")
			}
			require.Zero(t, d.hangups, "OnNotify handler set, so no implicit hangup")
		})
	}
}

// TestDialogHandleReferNotifyShortBody checks a sipfrag body too short to carry
// a status line is rejected rather than panicking on the slice.
func TestDialogHandleReferNotifyShortBody(t *testing.T) {
	d := &referNotifyDialog{media: &DialogMedia{}}
	req := newReferNotifyRequest(t, "message/sipfrag", "SIP/2.0")
	tx, conn := newReferNotifyTx(t, req)

	dialogHandleReferNotify(d, req, tx)

	require.Len(t, conn.msgs, 1)
	res := conn.msgs[0].(*sip.Response)
	require.Equal(t, sip.StatusBadRequest, res.StatusCode)
}
