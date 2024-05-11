package diago

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/sipgox"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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
	Session *sipgox.MediaSession

	*sipgox.RTPWriter
	*sipgox.RTPReader
}

// Just to satisfy DialogSession interface
func (d *DialogMedia) Media() *DialogMedia {
	return d
}

// MediaPCMDecoder wraps RTP reader and decodes current codec to PCM
// You can think it as translator.
// PCMDecoder is just io.Reader and it returns payload as decoded. Consider that size of PCM payloads will be bigger
func (d *DialogMedia) PCMDecoder() (*audio.PCMDecoder, error) {
	return audio.NewPCMDecoder(d.RTPReader.PayloadType, d.RTPReader)
}

func (d *DialogMedia) PCMEncoder() (*audio.PCMEncoder, error) {
	return audio.NewPCMEncoder(d.RTPWriter.PayloadType, d.RTPWriter)
}

func (d *DialogMedia) PlaybackCreate() (*Playback, error) {
	rtpWriter := d.RTPWriter
	pt := rtpWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return nil, err
	}

	p := Playback{
		writer: audio.NewPlaybackControlWriter(enc),
	}
	return &p, nil
}

func (d *DialogMedia) PlaybackFile(filename string) error {
	if d.Session == nil {
		return fmt.Errorf("call not answered")
	}

	p, err := d.PlaybackCreate()
	if err != nil {
		return err
	}

	_, err = p.PlayFile(filename)
	return err
}

func (d *DialogMedia) PlaybackURL(ctx context.Context, urlStr string) error {
	if d.Session == nil {
		return fmt.Errorf("call not answered")
	}

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
		return fmt.Errorf("Non 200 received. code=%d", res.StatusCode)
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
			return fmt.Errorf("Parsing audio size failed")
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
				return fmt.Errorf("Reading chunk stopped: %w", err)
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
					return fmt.Errorf("Failed to request range: %w", err)
				}

				if res.StatusCode == http.StatusRequestedRangeNotSatisfiable && res.ContentLength == 0 {
					break
				}

				if res.StatusCode != http.StatusPartialContent {
					return fmt.Errorf("expected partial content response: code=%d", res.StatusCode)
				}

				chunk, err := io.ReadAll(res.Body)
				if err != nil {
					return fmt.Errorf("Reading chunk stopped: %w", err)
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

		written, err := streamWavRTP(reader, d.RTPWriter)

		// There is no reason having http goroutine still running
		// First make sure http goroutine exited and join errors
		err = errors.Join(<-httpErr, err)
		if err != nil {
			return err
		}

		if written == 0 {
			return fmt.Errorf("nothing written")
		}
		return err
	}

	// 	// We need some stream wave implementation
	samples, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	wavBuf := bytes.NewReader(samples)
	written, err := streamWavRTP(wavBuf, d.RTPWriter)
	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	return err
}
