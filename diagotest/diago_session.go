// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diagotest

import (
	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
)

func NewRequest(method sip.RequestMethod, recipient sip.Uri) (*sip.Request, error) {
	req := sip.NewRequest(method, recipient)
	ua, _ := sipgo.NewUA()
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientAddr("127.0.0.1:11111"))
	if method == sip.INVITE {
		req.AppendHeader(sip.NewHeader("Contact", "<sip:127.0.0.1:11111>"))
	}

	return req, sipgo.ClientRequestBuild(cli, req)
}

func NewDialogServerSession(inviteReq *sip.Request) (*diago.DialogServerSession, *siptest.ServerTxRecorder, error) {
	// TODO
	// Fake connection
	ua := sipgo.DialogUA{
		Client: &sipgo.Client{}, // Fake client
		ContactHDR: sip.ContactHeader{
			Address: sip.Uri{Scheme: "sip", User: "tester", Host: "127.0.0.1", Port: 5060},
		},
	}

	tx := siptest.NewServerTxRecorder(inviteReq)

	sess, err := ua.ReadInvite(inviteReq, tx)
	if err != nil {
		return nil, nil, err
	}

	d := diago.DialogServerSession{
		DialogServerSession: sess,
	}

	return &d, tx, nil
}
