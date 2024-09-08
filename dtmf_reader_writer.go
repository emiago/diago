// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"github.com/emiago/diago/media"
	"github.com/rs/zerolog/log"
)

type DTMFWriter struct {
	rtpWriter *media.RTPDtmfWriter
}

func (w *DTMFWriter) WriteDTMF(dtmf rune) error {
	return w.rtpWriter.WriteDTMF(dtmf)
}

type DTMFReader struct {
	rtpReader *media.RTPDtmfReader
}

func (r *DTMFReader) ReadDTMF() (rune, bool) {
	return r.rtpReader.ReadDTMF()
}

func (r *DTMFReader) OnDTMF(f func(dtmf rune)) {
	go func() {
		defer log.Debug().Msg("OnDTMF exited")
		for {
			dtmf, ok := r.ReadDTMF()
			if !ok {
				break
			}
			f(dtmf)
		}
	}()
}
