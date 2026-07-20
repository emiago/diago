// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package mediaweb

import (
	"errors"
	"fmt"

	"github.com/pion/rtcp"
)

var errRTCPFailedToUnmarshal = errors.New("rtcp: failed to unmarshal")

func rtcpUnmarshal(data []byte, packets []rtcp.Packet) (int, error) {
	n := 0
	for i := 0; i < len(packets) && len(data) != 0; i++ {
		var header rtcp.Header
		if err := header.Unmarshal(data); err != nil {
			return 0, errors.Join(err, errRTCPFailedToUnmarshal)
		}
		packetLength := int(header.Length+1) * 4
		if packetLength > len(data) {
			return 0, fmt.Errorf("packet too short: %w", errRTCPFailedToUnmarshal)
		}
		packet := rtcpPacket(header.Type)
		if err := packet.Unmarshal(data[:packetLength]); err != nil {
			return 0, err
		}
		packets[i] = packet
		data = data[packetLength:]
		n++
	}
	return n, nil
}

func rtcpPacket(packetType rtcp.PacketType) rtcp.Packet {
	switch packetType {
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
