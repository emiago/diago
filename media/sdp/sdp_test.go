// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package sdp

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSDP(t *testing.T) {
	// t.Skip("TODO: fix SDP unmarshal")
	body := `v=0
o=- 3905350750 3905350750 IN IP4 192.168.100.11
s=pjmedia
b=AS:84
t=0 0
a=X-nat:0
m=audio 57797 RTP/AVP 96 97 98 99 3 0 8 9 120 121 122
c=IN IP4 192.168.100.11
b=TIAS:64000
a=sendrecv
a=rtpmap:96 speex/16000
a=rtpmap:97 speex/8000
a=rtpmap:98 speex/32000
a=rtpmap:99 iLBC/8000
a=fmtp:99 mode=30
a=rtpmap:120 telephone-event/16000
a=fmtp:120 0-16
a=rtpmap:121 telephone-event/8000
a=fmtp:121 0-16
a=rtpmap:122 telephone-event/32000
a=fmtp:122 0-16
a=ssrc:1204560450 cname:4585300731f880ff
a=rtcp:57798 IN IP4 192.168.100.11
a=rtcp-mux`

	// Fails due to b=TIAS:64000
	sd := SessionDescription{}
	err := Unmarshal([]byte(body), &sd)
	require.NoError(t, err)

	require.Equal(t, sd.Value("m"), "audio 57797 RTP/AVP 96 97 98 99 3 0 8 9 120 121 122")

	md, err := sd.MediaDescription("audio")
	require.NoError(t, err)
	require.Equal(t, 57797, md.Port)
	require.Equal(t, "RTP/AVP", md.Proto)
	require.Equal(t, []string{"96", "97", "98", "99", "3", "0", "8", "9", "120", "121", "122"}, md.Formats)

	ci, err := sd.ConnectionInformation()
	require.NoError(t, err)
	require.Equal(t, "IN", ci.NetworkType)
	require.Equal(t, "IP4", ci.AddressType)
	require.Equal(t, net.ParseIP("192.168.100.11").String(), ci.IP.String())

}
