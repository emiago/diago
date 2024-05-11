package diago

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/sipgox"
	"github.com/go-audio/riff"
	"github.com/rs/zerolog/log"
)

type Playback struct {
	// reader io.Reader
	// TODO we could avoid mute controller
	writer *audio.PlaybackControl
}

func (p *Playback) Mute(mute bool) {
	p.writer.Mute(mute)
}

func (p *Playback) Stop() {
	p.writer.Stop()
}

// func (p *Playback) StreamWav(reader io.Reader) error {

// }

func (p *Playback) PlayFile(filename string) (written int, err error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	return streamWav(file, p.writer)
}

// func (p *Playback) PlayFileBackground(filename string) chan error {
// 	ch := make(chan error)
// 	go func() {
// 		ch <- p.PlayFile(filename)
// 	}()
// 	return ch
// }

// type Playback struct {
// 	reader *audio.PlaybackControl
// }

// func (p *Playback) Mute(mute bool) {
// 	p.reader.Mute(mute)
// }

// func (p *Playback) Mute(mute bool) {
// 	p.reader.Mute(mute)
// }

// With control stream audio can be muted or unmuted
func NewControlStream(m *DialogMedia) *audio.PlaybackControl {
	playback := audio.NewPlaybackControl(m.RTPReader, m.RTPWriter)
	return playback
}

/*
	 func copyWavRTP(body io.ReadSeeker, rtpWriter *sipgox.RTPWriter) (int, error) {
		dec := aud.NewWavDecoder(body)
		dec.ReadInfo()
		if dec.BitDepth != 16 {
			return 0, fmt.Errorf("received bitdepth=%d, but only 16 bit PCM supported", dec.BitDepth)
		}
		if dec.SampleRate != 8000 {
			return 0, fmt.Errorf("only 8000 sample rate supported")
		}

		pt := rtpWriter.PayloadType
		enc, err := aud.NewPCMEncoder(pt, rtpWriter)
		if err != nil {
			return 0, err
		}

		// We need to read and packetize to 20 ms
		sampleDurMS := 20
		payloadBuf := make([]byte, int(dec.BitDepth)/8*int(dec.NumChans)*int(dec.SampleRate)/1000*sampleDurMS) // 20 ms

		ticker := time.NewTicker(time.Duration(sampleDurMS) * time.Millisecond)
		defer ticker.Stop()
		totalWritten := 0

outloop:

		for {
			if err := dec.FwdToPCM(); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return 0, err
			}

			for {
				n, err := dec.PCMChunk.Read(payloadBuf)
				if err != nil {
					if errors.Is(err, io.EOF) {
						break outloop
					}
					return 0, err
				}

				// Ticker has already correction for slow operation so this is enough
				<-ticker.C
				n, err = enc.Write(payloadBuf[:n])
				if err != nil {
					return 0, err
				}
				totalWritten += n
			}
		}
		return totalWritten, nil
	}
*/
func streamWavRTP(body io.Reader, rtpWriter *sipgox.RTPWriter) (int, error) {
	pt := rtpWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return 0, err
	}
	return streamWav(body, enc)
}

func streamWav(body io.Reader, enc io.Writer) (int, error) {
	dec := audio.NewWavDecoderStreamer(body)
	if err := dec.ReadHeaders(); err != nil {
		return 0, err
	}
	if dec.BitsPerSample != 16 {
		return 0, fmt.Errorf("received bitdepth=%d, but only 16 bit PCM supported", dec.BitsPerSample)
	}
	if dec.SampleRate != 8000 {
		return 0, fmt.Errorf("only 8000 sample rate supported")
	}

	// We need to read and packetize to 20 ms
	sampleDurMS := 20
	payloadBuf := make([]byte, int(dec.BitsPerSample)/8*int(dec.NumChannels)*int(dec.SampleRate)/1000*sampleDurMS) // 20 ms

	ticker := time.NewTicker(time.Duration(sampleDurMS) * time.Millisecond)
	defer ticker.Stop()
	totalWritten := 0
outloop:
	for {
		ch, err := dec.NextChunk()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, err
		}

		// Why this is never the case
		if ch.ID != riff.DataFormatID && ch.ID != [4]byte{} {
			// Until we reach data chunk we will draining
			ch.Drain()
			continue
		}

		for {
			n, err := ch.Read(payloadBuf)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break outloop
				}
				return 0, err
			}

			// Ticker has already correction for slow operation so this is enough
			<-ticker.C
			n, err = enc.Write(payloadBuf[:n])
			if err != nil {
				return 0, err
			}
			totalWritten += n
		}
	}
	return totalWritten, nil
}

type loggingTransport struct{}

func (s *loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	bytes, _ := httputil.DumpRequestOut(r, false)

	resp, err := http.DefaultTransport.RoundTrip(r)
	// err is returned after dumping the response

	respBytes, _ := httputil.DumpResponse(resp, false)
	bytes = append(bytes, respBytes...)

	log.Debug().Msgf("HTTP Debug:\n%s\n", bytes)

	return resp, err
}
