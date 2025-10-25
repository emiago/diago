// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package ed137

import (
	"errors"
	"sync"

	"github.com/emiago/diago/media"
)

var (
	rtpExtPTTOff = RTPTxExtension{PTTType: PTT_OFF, PTTID: 1}.Marshal()
	rtpExtPTTOn  = RTPTxExtension{PTTType: PTT_NORMAL_ON, PTTID: 1}.Marshal()
)

type RTPED137Writer struct {
	packetWriter *media.RTPPacketWriter

	mu     sync.Mutex
	ptt    uint8
	pttExt []byte
}

// RTPED137Writer requires packetizer writer to be in pipeline
// When doing write it will arrange different packetization
func NewRTPED137Writer(rtpPacketizer *media.RTPPacketWriter) *RTPED137Writer {
	return &RTPED137Writer{
		packetWriter: rtpPacketizer,
	}
}

// Write is RTP io.Writer which adds more sync mechanism
func (w *RTPED137Writer) Write(b []byte) (int, error) {
	// If locked it means writer is currently writing DTMF over same stream
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ptt == PTT_OFF {
		// When PTT_OFF no audio should be passed
		b = []byte{}
	}

	return w.packetWriter.WriteWithExt(b, 0, w.pttExt, ED137ProfileType)
}

func (w *RTPED137Writer) PTT(val uint8) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ptt = val
	w.pttExt = RTPTxExtension{PTTType: val, PTTID: 1}.Marshal()
}

type RTPED137Reader struct {
	packetReader *media.RTPPacketReader
	lastPtt      uint8
	OnPTTChange  func(ptt uint8) error
}

func NewRTPED137Reader(rtpPacketizer *media.RTPPacketReader) *RTPED137Reader {
	return &RTPED137Reader{
		packetReader: rtpPacketizer,
	}
}

// Write is RTP io.Writer which adds more sync mechanism
func (w *RTPED137Reader) Read(b []byte) (int, error) {
	// If locked it means writer is currently writing DTMF over same stream
	n, err := w.packetReader.Read(b)
	if err != nil {
		return n, err
	}

	if w.OnPTTChange != nil {
		err = func() error {
			ptt, err := w.ReadPTT()
			if err != nil {
				if err == errNoPTTExt {
					// Continue
					return nil
				}
				return err
			}
			if ptt == w.lastPtt {
				// same
				return nil
			}
			w.lastPtt = ptt
			return w.OnPTTChange(ptt)
		}()
	}

	return n, err
}

var (
	errNoPTTExt = errors.New("no RTP PTT extension")
)

func (w *RTPED137Reader) ReadPTT() (uint8, error) {
	ext := w.packetReader.PacketHeader.GetExtension(0)
	if ext == nil {
		return 0, errNoPTTExt
	}

	var rtpx RTPTxExtension
	if err := rtpx.Unmarshal(ext); err != nil {
		return 0, err
	}

	return rtpx.PTTType, nil
}
