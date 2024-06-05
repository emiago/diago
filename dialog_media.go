package diago

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/media"
)

var (
	HTTPDebug = os.Getenv("HTTP_DEBUG") == "true"
	// TODO remove client singleton
	client = http.Client{
		Timeout: 10 * time.Second,
	}
)

func init() {
	if HTTPDebug {
		client.Transport = &loggingTransport{}
	}
}

// DialogMedia is io.ReaderWriter for RTP. By default it exposes RTP Read and Write.
type DialogMedia struct {
	// DO NOT use IT or mix with reader and writer, unless it is specific case
	Session *media.MediaSession

	*media.RTPWriter
	*media.RTPReader
}

// Just to satisfy DialogSession interface
func (d *DialogMedia) Media() *DialogMedia {
	return d
}

// MediaPCMDecoder wraps RTP reader and decodes current codec to PCM
// You can think it as translator.
// PCMDecoder is just io.Reader and it returns payload as decoded. Consider that size of PCM payloads will be bigger
// func (d *DialogMedia) PCMDecoder() (*audio.PCMDecoder, error) {
// 	return audio.NewPCMDecoder(d.RTPReader.PayloadType, d.RTPReader)
// }

// func (d *DialogMedia) PCMEncoder() (*audio.PCMEncoder, error) {
// 	return audio.NewPCMEncoder(d.RTPWriter.PayloadType, d.RTPWriter)
// }

func (d *DialogMedia) PlaybackCreate() (Playback, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap

	rtpWriter := d.RTPWriter
	pt := rtpWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return Playback{}, err
	}

	p := Playback{
		writer: enc,
	}
	return p, nil
}

func (d *DialogMedia) PlaybackControlCreate() (PlaybackControl, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap
	rtpWriter := d.RTPWriter
	pt := rtpWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return PlaybackControl{}, err
	}

	// Audio is controled via audio reader/writer
	control := &audio.PlaybackControl{
		Writer: enc,
	}

	p := PlaybackControl{
		Playback: Playback{
			writer: control,
		},
		control: control,
	}
	return p, nil
}

func (d *DialogMedia) PlaybackFile(filename string) error {
	if d.Session == nil {
		return fmt.Errorf("call not answered")
	}

	p, err := d.PlaybackCreate()
	if err != nil {
		return err
	}

	err = p.PlayFile(filename)
	return err
}

func (d *DialogMedia) PlaybackURL(ctx context.Context, urlStr string) error {
	if d.Session == nil {
		return fmt.Errorf("call not answered")
	}

	p, err := d.PlaybackCreate()
	if err != nil {
		return err
	}

	err = p.PlayURL(ctx, urlStr)
	return err
}
