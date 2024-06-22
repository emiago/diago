package diago

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/media"
	"github.com/go-audio/riff"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Playback struct {
	// reader io.Reader
	// TODO we could avoid mute controller
	writer io.Writer

	totalWritten int
}

func (p *Playback) Play(reader io.Reader, mimeType string) error {
	switch mimeType {
	case "audio/wav", "audio/x-wav", "audio/wav-x", "audio/vnd.wave":
	default:
		return fmt.Errorf("unsuported content type %q", mimeType)
	}

	written, err := streamWav(reader, p.writer)
	if err != nil {
		return err
	}

	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	p.totalWritten += written
	return nil
}

func (p *Playback) PlayFile(filename string) (err error) {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	return p.Play(file, "audio/wav")
}

func (p *Playback) PlayURL(ctx context.Context, urlStr string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Range", "bytes=0-1023") // Try with range request

	res, err := client.Do(req)
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("non 200 received. code=%d", res.StatusCode)
	}

	contType := res.Header.Get("Content-Type")
	mimeType, _, err := mime.ParseMediaType(contType)
	if err != nil {
		return err
	}

	switch mimeType {
	case "audio/wav", "audio/x-wav", "audio/wav-x", "audio/vnd.wave":
	default:
		return fmt.Errorf("unsuported content type %q", contType)
	}

	// Check can be streamed
	if res.StatusCode == http.StatusPartialContent {
		// acceptRanges := res.Header.Get("Accept-Ranges")
		// if acceptRanges != "bytes" {
		// 	return fmt.Errorf("header Accept-Ranges != bytes. Value=%q", acceptRanges)
		// }

		contentRange := res.Header.Get("Content-Range")
		ind := strings.LastIndex(contentRange, "/")
		if ind < 0 {
			return fmt.Errorf("full audio size in Content-Range not present")
		}
		maxSize, err := strconv.ParseInt(contentRange[ind+1:], 10, 64)
		if err != nil {
			return err
		}

		if maxSize <= 0 {
			return fmt.Errorf("parsing audio size failed")
		}
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Range_requests

		// WAV header size is 44 bytes so we have more than enough

		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()

		// BETTER DESIGN needed

		httpPartial := func(log zerolog.Logger, res *http.Response, writer io.Writer) error {
			chunk, err := io.ReadAll(res.Body)
			if err != nil {
				return fmt.Errorf("reading chunk stopped: %w", err)
			}
			res.Body.Close()

			if _, err := writer.Write(chunk); err != nil {
				return err
			}

			var start int64 = 1024
			var offset int64 = 64 * 1024 // 512K
			for ; start < maxSize; start += offset {
				end := min(start+offset-1, maxSize)
				log.Debug().Int64("start", start).Int64("end", end).Int64("max", maxSize).Msg("Reading chunk size")
				// Range is inclusive
				rangeHDR := fmt.Sprintf("bytes=%d-%d", start, end)

				req.Header.Set("Range", rangeHDR) // Try with range request
				res, err = client.Do(req)
				if err != nil {
					return fmt.Errorf("failed to request range: %w", err)
				}

				if res.StatusCode == http.StatusRequestedRangeNotSatisfiable && res.ContentLength == 0 {
					break
				}

				if res.StatusCode != http.StatusPartialContent {
					return fmt.Errorf("expected partial content response: code=%d", res.StatusCode)
				}

				chunk, err := io.ReadAll(res.Body)
				if err != nil {
					return fmt.Errorf("reading chunk stopped: %w", err)
				}
				res.Body.Close()

				if _, err := writer.Write(chunk); err != nil {
					return err
				}
			}
			return nil
		}

		httpErr := make(chan error)
		go func() {
			// TODO have here context logger
			err := httpPartial(log.Logger, res, writer)
			writer.Close()
			httpErr <- err
		}()

		written, err := streamWav(reader, p.writer)

		// There is no reason having http goroutine still running
		// First make sure http goroutine exited and join errors
		err = errors.Join(<-httpErr, err)
		if err != nil {
			return err
		}

		if written == 0 {
			return fmt.Errorf("nothing written")
		}
		p.totalWritten += written
		return err
	}

	// 	// We need some stream wave implementation
	samples, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	wavBuf := bytes.NewReader(samples)
	written, err := streamWav(wavBuf, p.writer)
	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	p.totalWritten += written
	return err
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
func streamWavRTP(body io.Reader, rtpWriter *media.RTPWriter) (int, error) {
	pt := rtpWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return 0, err
	}
	return streamWav(body, enc)
}

func streamWav(body io.Reader, enc io.Writer) (int, error) {
	// dec := audio.NewWavDecoderStreamer(body)
	dec := audio.NewWavReader(body)
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
