// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"errors"
	"fmt"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

var (
	errRTCPFailedToUnmarshal = errors.New("rtcp: failed to unmarshal")
	errRTPPayloadTooSmall    = errors.New("rtp: payload size is too small")
)

// RTPUnmarshal wrapper for now used for optimizing unmarshal
// NOTE: payload is referenced in packet buffer
func RTPUnmarshal(buf []byte, p *rtp.Packet) error {
	return p.Unmarshal(buf)
}

func rtpUnmarshalPayload(buf []byte, p *rtp.Packet) error {
	n := p.Header.MarshalSize()

	// extracted from pion non header unmarshal
	end := len(buf)
	if p.Header.Padding {
		if end <= n {
			return errRTPPayloadTooSmall
		}
		p.Header.PaddingSize = buf[end-1]
		end -= int(p.Header.PaddingSize)
	} else {
		p.Header.PaddingSize = 0
	}
	p.PaddingSize = p.Header.PaddingSize
	if end < n {
		return errRTPPayloadTooSmall
	}

	p.Payload = buf[n:end]
	return nil
}

// RTCPUnmarshal is improved version based on pion/rtcp where we allow caller to define and control
// buffer of rtcp packets. This also reduces one allocation
// NOTE: data is still referenced in packet buffer
func RTCPUnmarshal(data []byte, packets []rtcp.Packet) (n int, err error) {
	for i := 0; i < len(packets) && len(data) != 0; i++ {
		var h rtcp.Header

		err = h.Unmarshal(data)
		if err != nil {
			// fmt.Errorf("unmarshal RTCP error: %w", err)
			return 0, errors.Join(err, errRTCPFailedToUnmarshal)
		}

		pktLen := int(h.Length+1) * 4
		if pktLen > len(data) {
			return 0, fmt.Errorf("packet too short: %w", errRTCPFailedToUnmarshal)
		}
		inPacket := data[:pktLen]

		// Check the type and unmarshal
		packet := rtcpTypedPacket(h.Type)
		err = packet.Unmarshal(inPacket)
		if err != nil {
			return 0, err
		}

		packets[i] = packet

		data = data[pktLen:]
		n++
	}

	return n, nil
}

func rtcpMarshal(packets []rtcp.Packet) ([]byte, error) {
	return rtcp.Marshal(packets)
}

// TODO this would be nice that pion exports
func rtcpTypedPacket(htype rtcp.PacketType) rtcp.Packet {
	// Currently we are not interested

	switch htype {
	case rtcp.TypeSenderReport:
		return new(rtcp.SenderReport)

	case rtcp.TypeReceiverReport:
		return new(rtcp.ReceiverReport)

	case rtcp.TypeSourceDescription:
		return new(rtcp.SourceDescription)

	case rtcp.TypeGoodbye:
		return new(rtcp.Goodbye)

	default:
		return new(rtcp.RawPacket)
	}
}
