// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package ed137

import (
	"testing"

	"github.com/pion/rtp"
)

// --- Helpers to build RTP packets ---

func setRTPTxExtension(pkt *rtp.Packet, ext RTPTxExtension) {
	pkt.Extension = true
	pkt.ExtensionProfile = ED137ProfileType
	if err := pkt.SetExtension(0, ext.Marshal()); err != nil {
		panic(err)
	}
}

func setAudioPayload(pkt *rtp.Packet, audio []byte) {
	pkt.Payload = audio
}

// --- The actual ED-137 test ---

func TestED137_RTPTx_Behavior(t *testing.T) {
	tests := []struct {
		name      string
		ext       RTPTxExtension
		audio     []byte
		wantAudio bool
		wantPTT   uint8
	}{
		{
			name:      "PTT OFF – no audio",
			ext:       RTPTxExtension{PTTType: PTT_OFF, PTTID: 1},
			audio:     nil,
			wantAudio: false,
			wantPTT:   PTT_OFF,
		},
		{
			name:      "PTT ON – no audio (keep-alive)",
			ext:       RTPTxExtension{PTTType: PTT_NORMAL_ON, PTTID: 1},
			audio:     nil,
			wantAudio: false,
			wantPTT:   PTT_NORMAL_ON,
		},
		{
			name:      "PTT ON – with audio samples",
			ext:       RTPTxExtension{PTTType: PTT_NORMAL_ON, PTTID: 1},
			audio:     []byte{0x11, 0x22, 0x33, 0x44},
			wantAudio: true,
			wantPTT:   PTT_NORMAL_ON,
		},
		{
			name:      "PTT OFF again",
			ext:       RTPTxExtension{PTTType: PTT_OFF, PTTID: 1},
			audio:     nil,
			wantAudio: false,
			wantPTT:   PTT_OFF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var pkt rtp.Packet
			pkt.Version = 2
			pkt.PayloadType = 0 // G.711 µ-law
			pkt.SequenceNumber = 1234
			pkt.Timestamp = 16000
			pkt.SSRC = 0x11223344

			setRTPTxExtension(&pkt, tc.ext)
			setAudioPayload(&pkt, tc.audio)

			raw, err := pkt.Marshal()
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var decoded rtp.Packet
			if err := decoded.Unmarshal(raw); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			// Validate RTP header
			if decoded.Version != 2 {
				t.Errorf("invalid RTP version: got %d", decoded.Version)
			}
			if decoded.ExtensionProfile != ED137ProfileType {
				t.Errorf("wrong ExtensionProfile: got 0x%X want 0x%X",
					decoded.ExtensionProfile, ED137ProfileType)
			}

			// Decode ED-137 header extension
			var info RTPTxExtension
			if len(decoded.Extensions) == 0 {
				t.Fatalf("missing ED137 header extension")
			}
			info.Unmarshal(decoded.GetExtension(0))

			if info.PTTType != tc.wantPTT {
				t.Errorf("PTTType mismatch: got 0x%02X want 0x%02X",
					info.PTTType, tc.wantPTT)
			}

			gotAudio := len(decoded.Payload) > 0
			if gotAudio != tc.wantAudio {
				t.Errorf("audio presence mismatch: got %v want %v", gotAudio, tc.wantAudio)
			}
		})
	}
}
