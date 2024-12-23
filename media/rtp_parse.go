// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"errors"
	"fmt"
	"io"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

var (
	errRTCPFailedToUnmarshal = errors.New("rtcp: failed to unmarshal")
)

// Experimental
//
// RTPUnmarshal temporarly solution to provide more optimized unmarshal version based on pion/rtp
// it does not preserve any buffer reference which allows reusage
//
// TODO build RTP header unmarshaller for VOIP needs
func RTPUnmarshal(buf []byte, p *rtp.Packet) error {
	n, err := p.Header.Unmarshal(buf)
	if err != nil {
		return err
	}
	if p.Header.Extension {
		// For now eliminate it as it holds reference on buffer
		// TODO fix this
		p.Header.Extensions = nil
		p.Header.Extension = false
	}

	end := len(buf)
	if p.Header.Padding {
		p.PaddingSize = buf[end-1]
		end -= int(p.PaddingSize)
	}
	if end < n {
		return io.ErrShortBuffer
	}

	// If Payload buffer exists try to fill it and allow buffer reusage
	if p.Payload != nil && len(p.Payload) >= len(buf[n:end]) {
		copy(p.Payload, buf[n:end])
		return nil
	}

	// This creates allocations
	// Payload should be recreated instead referenced
	// This allows buf reusage
	p.Payload = make([]byte, len(buf[n:end]))
	copy(p.Payload, buf[n:end])
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
