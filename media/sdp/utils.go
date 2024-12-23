// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package sdp

import (
	"fmt"
	"net"
	"strings"
	"time"
)

func GetCurrentNTPTimestamp() uint64 {
	var ntpEpochOffset int64 = 2208988800 // Offset from Unix epoch (January 1, 1970) to NTP epoch (January 1, 1900)
	currentTime := time.Now().Unix() + int64(ntpEpochOffset)

	return uint64(currentTime)
}

func NTPTimestamp(now time.Time) uint64 {
	var ntpEpochOffset int64 = 2208988800 // Offset from Unix epoch (January 1, 1970) to NTP epoch (January 1, 1900)
	currentTime := now.Unix() + ntpEpochOffset

	return uint64(currentTime)
}

type Mode string

const (
	// https://datatracker.ietf.org/doc/html/rfc4566#section-6
	ModeRecvonly Mode = "recvonly"
	ModeSendrecv Mode = "sendrecv"
	ModeSendonly Mode = "sendonly"
)

// GenerateForAudio is minimal AUDIO SDP setup
func GenerateForAudio(originIP net.IP, connectionIP net.IP, rtpPort int, mode Mode, fmts Formats) []byte {
	ntpTime := GetCurrentNTPTimestamp()

	formatsMap := []string{}
	for _, f := range fmts {
		switch f {
		case FORMAT_TYPE_ULAW:
			formatsMap = append(formatsMap, "a=rtpmap:0 PCMU/8000")
		case FORMAT_TYPE_ALAW:
			formatsMap = append(formatsMap, "a=rtpmap:8 PCMA/8000")
		case FORMAT_TYPE_OPUS:
			formatsMap = append(formatsMap, "a=rtpmap:96 opus/48000/2")
			// Providing 0 when FEC cannot be used on the receiving side is RECOMMENDED.
			// https://datatracker.ietf.org/doc/html/rfc7587
			formatsMap = append(formatsMap, "a=fmtp:96 useinbandfec=0")
		case FORMAT_TYPE_TELEPHONE_EVENT:
			formatsMap = append(formatsMap, "a=rtpmap:101 telephone-event/8000")
			formatsMap = append(formatsMap, "a=fmtp:101 0-16")
		}
	}

	// Support only ulaw and alaw
	// TODO optimize this with string builder
	s := []string{
		"v=0",
		fmt.Sprintf("o=- %d %d IN IP4 %s", ntpTime, ntpTime, originIP),
		"s=Sip Go Media",
		// "b=AS:84",
		fmt.Sprintf("c=IN IP4 %s", connectionIP),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %s", rtpPort, strings.Join(fmts, " ")),
	}

	s = append(s, formatsMap...)
	s = append(s,
		"a=ptime:20", // Needed for opus
		"a=maxptime:20",
		"a="+string(mode))
	// s := []string{
	// 	"v=0",
	// 	fmt.Sprintf("o=- %d %d IN IP4 %s", ntpTime, ntpTime, originIP),
	// 	"s=Sip Go Media",
	// 	// "b=AS:84",
	// 	fmt.Sprintf("c=IN IP4 %s", connectionIP),
	// 	"t=0 0",
	// 	fmt.Sprintf("m=audio %d RTP/AVP 96 97 98 99 3 0 8 9 120 121 122", rtpPort),
	// 	"a=" + string(mode),
	// 	"a=rtpmap:96 speex/16000",
	// 	"a=rtpmap:97 speex/8000",
	// 	"a=rtpmap:98 speex/32000",
	// 	"a=rtpmap:99 iLBC/8000",
	// 	"a=fmtp:99 mode=30",
	// 	"a=rtpmap:120 telephone-event/16000",
	// 	"a=fmtp:120 0-16",
	// 	"a=rtpmap:121 telephone-event/8000",
	// 	"a=fmtp:121 0-16",
	// 	"a=rtpmap:122 telephone-event/32000",
	// 	"a=rtcp-mux",
	// 	fmt.Sprintf("a=rtcp:%d IN IP4 %s", rtpPort+1, connectionIP),
	// }

	res := strings.Join(s, "\r\n")
	return []byte(res)
}
