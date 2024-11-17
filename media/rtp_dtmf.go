// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"encoding/binary"
	"fmt"
)

// DTMF event mapping (RFC 4733)
var dtmfEventMapping = map[rune]byte{
	'0': 0,
	'1': 1,
	'2': 2,
	'3': 3,
	'4': 4,
	'5': 5,
	'6': 6,
	'7': 7,
	'8': 8,
	'9': 9,
	'*': 10,
	'#': 11,
	'A': 12,
	'B': 13,
	'C': 14,
	'D': 15,
}

var dtmfEventMappingRev = map[byte]rune{
	0:  '0',
	1:  '1',
	2:  '2',
	3:  '3',
	4:  '4',
	5:  '5',
	6:  '6',
	7:  '7',
	8:  '8',
	9:  '9',
	10: '*',
	11: '#',
	12: 'A',
	13: 'B',
	14: 'C',
	15: 'D',
}

func DTMFToRune(dtmf uint8) rune {
	return dtmfEventMappingRev[dtmf]
}

// RTPDTMFEncode creates series of DTMF redudant events which should be encoded as payload
// It is currently only 8000 sample rate considered for telophone event
func RTPDTMFEncode(char rune) []DTMFEvent {
	event := dtmfEventMapping[char]

	events := make([]DTMFEvent, 7)

	for i := 0; i < 4; i++ {
		d := DTMFEvent{
			Event:      event,
			EndOfEvent: false,
			Volume:     10,
			Duration:   160 * (uint16(i) + 1),
		}
		events[i] = d
	}

	// End events with redudancy
	for i := 4; i < 7; i++ {
		d := DTMFEvent{
			Event:      event,
			EndOfEvent: true,
			Volume:     10,
			Duration:   160 * 5, // Must not be increased for end event
		}
		events[i] = d
	}

	return events
}

// DTMFEvent represents a DTMF event
type DTMFEvent struct {
	Event      uint8
	EndOfEvent bool
	Volume     uint8
	Duration   uint16
}

func (ev *DTMFEvent) String() string {
	out := "RTP DTMF Event:\n"
	out += fmt.Sprintf("\tEvent: %d\n", ev.Event)
	out += fmt.Sprintf("\tEndOfEvent: %v\n", ev.EndOfEvent)
	out += fmt.Sprintf("\tVolume: %d\n", ev.Volume)
	out += fmt.Sprintf("\tDuration: %d\n", ev.Duration)
	return out
}

// DecodeRTPPayload decodes an RTP payload into a DTMF event
func DTMFDecode(payload []byte, d *DTMFEvent) error {
	if len(payload) < 4 {
		return fmt.Errorf("payload too short")
	}

	d.Event = payload[0]
	d.EndOfEvent = payload[1]&0x80 != 0
	d.Volume = payload[1] & 0x7F
	d.Duration = binary.BigEndian.Uint16(payload[2:4])
	// d.Duration = uint16(payload[2])<<8 | uint16(payload[3])
	return nil
}

func DTMFEncode(d DTMFEvent) []byte {
	header := make([]byte, 4)
	header[0] = d.Event

	if d.EndOfEvent {
		header[1] = 0x80
	}
	header[1] |= d.Volume & 0x3F
	binary.BigEndian.PutUint16(header[2:4], d.Duration)
	return header
}
