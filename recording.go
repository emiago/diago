package diago

import (
	"errors"
	"fmt"
	"io"
	"os"

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

func newDialogRecordingWav(wawFile *os.File, ar io.Reader, arProps MediaProps, aw io.Writer, awProps MediaProps) (AudioStereoRecordingWav, error) {

	if arProps.Codec != awProps.Codec {
		return AudioStereoRecordingWav{}, fmt.Errorf("codecs of reader and writer need to match for stereo")
	}
	codec := awProps.Codec
	// Create wav file to store recording
	// Now create WavWriter to have Wav Container written
	wavWriter := audio.NewWavWriter(wawFile)

	mon := audio.MonitorPCMStereo{}
	if err := mon.Init(wavWriter, codec, ar, aw); err != nil {
		wavWriter.Close()
		return AudioStereoRecordingWav{}, err
	}

	r := AudioStereoRecordingWav{
		wawWriter: wavWriter,
		mon:       mon,
	}
	return r, nil

}
