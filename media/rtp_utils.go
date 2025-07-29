// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

var ntpEpochOffset int64 = 2208988800

var rtpBufPool = sync.Pool{
	New: func() any {
		return make([]byte, RTPBufSize)
	},
}

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

// Function to generate a silent audio frame
func generateSilentAudioFrame() []byte {
	frame := make([]byte, 160) // 160 bytes for a 20ms frame at 8kHz

	// Fill the frame with silence (zero values)
	for i := 0; i < len(frame); i++ {
		frame[i] = 0
	}

	return frame
}

func ReadAll(reader io.Reader, sampleSize int) ([]byte, error) {
	total := []byte{}
	buf := make([]byte, sampleSize)
	for {
		n, err := reader.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		total = append(total, buf[:n]...)
	}
	return total, nil
}

func WriteAll(w io.Writer, data []byte, sampleSize int) (int64, error) {
	var total int64
	for i := 0; i < len(data); i += sampleSize {
		off := min(len(data), i+sampleSize)
		n, err := w.Write(data[i:off])
		if err != nil {
			return 0, err
		}
		total += int64(n)
	}
	return total, nil
}

// Copy is like io.Copy but it uses buffer size needed for RTP
func Copy(reader io.Reader, writer io.Writer) (int64, error) {
	return CopyWithBuf(reader, writer, make([]byte, RTPBufSize))
}

// CopyWithBuf is simple and strict compared to io.CopyBuffer. ReadFrom and WriteTo is not considered
// and due to RTP buf requirement it can lead to different buffer size passing
func CopyWithBuf(reader io.Reader, writer io.Writer, payloadBuf []byte) (int64, error) {
	var totalWritten int64
	for {
		n, err := reader.Read(payloadBuf)
		if err != nil {
			return totalWritten, err
		}
		nn, err := writer.Write(payloadBuf[:n])
		if err != nil {
			return totalWritten, err
		}
		totalWritten += int64(nn)
		if n < nn {
			return totalWritten, io.ErrShortWrite
		}
	}
}

func ErrorIsTimeout(err error) bool {
	e, ok := err.(net.Error)
	return ok && e.Timeout()
}
