// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func (p *AudioPlayback) PlayURL(urlStr string) (int64, error) {
	var written int64
	err := p.playURL(urlStr, &written)
	if errors.Is(err, io.EOF) {
		return written, nil
	}
	return written, err
}

func (p *AudioPlayback) playURL(urlStr string, written *int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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

		n, err := p.streamWav(reader, p.writer)
		*written = n
		p.totalWritten += n

		// There is no reason having http goroutine still running
		// First make sure http goroutine exited and join errors
		err = errors.Join(<-httpErr, err)
		return err
	}

	// 	// We need some stream wave implementation
	samples, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	wavBuf := bytes.NewReader(samples)
	n, err := p.streamWav(wavBuf, p.writer)
	*written = n
	p.totalWritten += n
	return err
}
