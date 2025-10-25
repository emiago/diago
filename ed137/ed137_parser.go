// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package ed137

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// ED-137 RTP Header Extension IDs
const (
	ED137ExtPTT = 1 // Example: Push-To-Talk status
	ED137ExtMIC = 2 // Example: Microphone activation / VOX
)

const (
	ED137ProfileType = 0x0067 // ED-137 Type field in RTP header extension
)

// PTT type values from ED-137 Table 12
const (
	PTT_OFF       = 0x00
	PTT_NORMAL_ON = 0x01
	PTT_COUPLING  = 0x02
	PTT_PRIORITY  = 0x03
	PTT_EMERGENCY = 0x04
)

// RTPTxExtension represents the 32-bit fixed info block (ED-137 §5.10.3)
type RTPTxExtension struct {
	PTTType uint8 // bits 0-2
	SQU     uint8 // bit 3
	PTTID   uint8 // bits 4-7
	SCT     uint8 // bit 8
	X       uint8 // bit 9 (extension marker)
	VF      uint8 // bit 31
}

// Marshal encodes the fixed 32-bit RTPTx information block
func (e RTPTxExtension) Marshal() []byte {
	buf := make([]byte, 4)
	_ = e.MarshalTo(buf)
	return buf
}
func (e RTPTxExtension) MarshalTo(buf []byte) error {
	var v uint32
	v |= uint32(e.PTTType & 0x07)  // bits 0–2
	v |= uint32(e.SQU&0x01) << 3   // bit 3
	v |= uint32(e.PTTID&0x0F) << 4 // bits 4–7
	v |= uint32(e.SCT&0x01) << 8   // bit 8
	v |= uint32(e.X&0x01) << 9     // bit 9
	v |= uint32(e.VF&0x01) << 31   // bit 31
	binary.BigEndian.PutUint32(buf, v)
	return nil
}

// Unmarshal decodes from 4 bytes into fields
func (e *RTPTxExtension) Unmarshal(b []byte) error {
	if len(b) < 4 {
		return fmt.Errorf("rtp extension is less than 4 bytes")
	}
	v := binary.BigEndian.Uint32(b)
	e.PTTType = uint8(v & 0x07)
	e.SQU = uint8((v >> 3) & 0x01)
	e.PTTID = uint8((v >> 4) & 0x0F)
	e.SCT = uint8((v >> 8) & 0x01)
	e.X = uint8((v >> 9) & 0x01)
	e.VF = uint8((v >> 31) & 0x01)
	return nil
}

func rtpTxExtension(h *rtp.Header, ext RTPTxExtension) error {
	h.Extension = true
	h.ExtensionProfile = ED137ProfileType
	return h.SetExtension(0, ext.Marshal())
}

// RTPPTTEnable builds RTP header extensions for ED-137
func RTPPTTEnable(h *rtp.Header, ptt bool) error {
	// PTT bit: 0x01 when pressed, 0x00 otherwise
	ext := RTPTxExtension{PTTType: PTT_OFF, PTTID: 1}
	if ptt {
		ext.PTTType = PTT_NORMAL_ON
	}

	return rtpTxExtension(h, ext)
}

func RTPMICEnable(rtpHeader *rtp.Header, micActive bool) error {
	micVal := []byte{0x00}
	if micActive {
		micVal[0] = 0x01
	}
	return rtpHeader.SetExtension(ED137ExtMIC, micVal)
}
