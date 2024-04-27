// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"net"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/rs/zerolog/log"
)

var ntpEpochOffset int64 = 2208988800

func GetCurrentNTPTimestamp() uint64 {
	now := time.Now()
	return NTPTimestamp(now)
}

func NTPTimestamp(t time.Time) uint64 {
	// Number of seconds since NTP epoch
	seconds := t.Unix() + ntpEpochOffset

	// Fractional part
	nanos := t.Nanosecond()
	frac := (float64(nanos) / 1e9) * (1 << 32)

	// NTP timestamp is 32bit second | 32 bit fractional
	ntpTimestamp := (uint64(seconds) << 32) | uint64(frac)

	return ntpTimestamp
}

func NTPToTime(ntpTimestamp uint64) time.Time {
	// NTP timestamp is 32bit second | 32 bit fractional
	seconds := int64(ntpTimestamp >> 32)                         // Upper 32 bits
	frac := float64(ntpTimestamp&0x00000000FFFFFFFF) / (1 << 32) // Lower 32 bits

	// Convert NTP seconds to Unix seconds
	unixSeconds := seconds - ntpEpochOffset
	nsec := int64(frac * 1e9)

	// Create a time.Time object
	return time.Unix(unixSeconds, nsec)
}

func SendDummyRTP(rtpConn *net.UDPConn, raddr net.Addr) {
	// Create an RTP packetizer for PCMU
	// Create an RTP packetizer
	mtu := uint16(1200)                    // Maximum Transmission Unit (MTU)
	payloadType := uint8(0)                // Payload type for PCMU
	ssrc := uint32(123456789)              // Synchronization Source Identifier (SSRC)
	payloader := &codecs.G711Payloader{}   // Payloader for PCMU
	sequencer := rtp.NewRandomSequencer()  // Sequencer for generating sequence numbers
	clockRate := uint32(8000)              // Audio clock rate for PCMU
	frameDuration := 20 * time.Millisecond // Duration of each audio frame

	packetizer := rtp.NewPacketizer(mtu, payloadType, ssrc, payloader, sequencer, clockRate)

	// Generate and send RTP packets every 20 milliseconds
	for {
		// Generate a dummy audio frame (replace with your actual audio data)
		audioData := generateSilentAudioFrame()

		// Calculate the number of samples
		numSamples := uint32(frameDuration.Seconds() * float64(clockRate))

		// Packetize the audio data into RTP packets
		packets := packetizer.Packetize(audioData, numSamples)

		// Send each RTP packet
		for _, packet := range packets {
			// Marshal the RTP packet into a byte slice
			data, err := packet.Marshal()
			if err != nil {
				log.Error().Err(err).Msg("Error marshaling RTP packet")
				continue
			}

			// Send the RTP packet
			if _, err := rtpConn.WriteTo(data, raddr); err != nil {
				log.Error().Err(err).Msg("Error sending RTP packet")
				return
			}

			log.Printf("Sent RTP packet: SeqNum=%d, Timestamp=%d, Payload=%d bytes\n", packet.SequenceNumber, packet.Timestamp, len(packet.Payload))
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// Function to generate a silent audio frame
func generateSilentAudioFrame() []byte {
	frame := make([]byte, 160) // 160 bytes for a 20ms frame at 8kHz

	// Fill the frame with silence (zero values)
	for i := 0; i < len(frame); i++ {
		frame[i] = 0
	}

	return frame
}
