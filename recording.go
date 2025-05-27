package diago

import (
	"errors"

	"github.com/emiago/diago/audio"
)

type AudioStereoRecordingWav struct {
	wawWriter *audio.WavWriter
	mon       audio.MonitorPCMStereo
}

func (r *AudioStereoRecordingWav) AudioReader() *audio.MonitorPCMStereo {
	return &r.mon
}

func (r *AudioStereoRecordingWav) AudioWriter() *audio.MonitorPCMStereo {
	return &r.mon
}

func (r *AudioStereoRecordingWav) Close() error {
	return errors.Join(
		r.mon.Close(),
		r.wawWriter.Close(),
	)
}
