package diago

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	aud "github.com/emiago/diago/audio"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
	"github.com/rs/zerolog/log"
)

// DialogServerSession represents inbound channel
type DialogServerSession struct {
	*sipgo.DialogServerSession
	DialogMedia
}

func (d *DialogServerSession) Close() {
	if d.mediaSession != nil {
		d.mediaSession.Close()
	}

	d.DialogServerSession.Close()
}

func (d *DialogServerSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

// User that was dialed
func (d *DialogServerSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}

func (d *DialogServerSession) Progress() error {
	return d.Respond(sip.StatusTrying, "Trying", nil)
}

func (d *DialogServerSession) Ringing() error {
	return d.Respond(sip.StatusRinging, "Ringing", nil)
}

func (d *DialogServerSession) Answer() error {
	// TODO, lot of here settings need to come from TU. or TU must copy before shipping
	// We may have this settings
	// - Codecs
	// - RTP port ranges

	// For now we keep things global and hardcoded
	// Codecs are ulaw,alaw
	// RTP port range is not set

	// Now media SETUP
	// ip, port, err := sipgox.FindFreeInterfaceHostPort("udp", "")
	// if err != nil {
	// 	return err
	// }
	ip, _, err := sip.ResolveInterfacesIP("ip4", nil)
	if err != nil {
		return err
	}

	laddr := &net.UDPAddr{IP: ip, Port: 0}
	sess, err := sipgox.NewMediaSession(laddr)
	if err != nil {
		return err
	}

	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.mediaSession = sess
	d.RTPReader = sipgox.NewRTPReader(sess)
	d.RTPWriter = sipgox.NewRTPWriter(sess)
	return d.RespondSDP(sess.LocalSDP())
}

func (d *DialogServerSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}

func (d *DialogServerSession) PlaybackFile(filename string) error {
	if d.MediaSession() == nil {
		return fmt.Errorf("call not answered")
	}

	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	written, err := streamWavRTP(file, d.RTPWriter)
	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	return err
}

func (d *DialogServerSession) PlaybackURL(urlStr string) error {
	if d.MediaSession() == nil {
		return fmt.Errorf("call not answered")
	}

	req, err := http.NewRequestWithContext(d.Context(), "GET", urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Range", "bytes=0-1023") // Try with range request

	http.DefaultClient.Timeout = 10 * time.Second
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("Non 200 received. code=%d", res.StatusCode)
	}

	contType := res.Header.Get("Content-Type")
	switch contType {
	case "audio/wav", "audio/x-wav", "audio/wav-x":
	default:
		return fmt.Errorf("unsuported content type %q", contType)
	}

	// Check can be streamed
	if res.StatusCode == http.StatusPartialContent {
		acceptRanges := res.Header.Get("Accept-Ranges")
		if acceptRanges != "bytes" {
			return fmt.Errorf("header Accept-Ranges != bytes")
		}

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

		go func() {
			// defer reader.Close()
			defer writer.Close()

			var start int64 = 1024
			var offset int64 = 64 * 1024 // 512K
			for ; start < maxSize; start += offset {
				end := max(start+offset-1, maxSize)

				log.Debug().Int64("start", start).Int64("end", end).Int64("max", maxSize).Msg("Reading chunk size")
				// First read current chunk
				chunk, err := io.ReadAll(res.Body)
				if err != nil {
					log.Error().Err(err).Msg("Reading chunk stopped")
					return
				}
				writer.Write(chunk)

				// Range is inclusive
				rangeHDR := fmt.Sprintf("bytes=%d-%d", start, end)

				req.Header.Set("Range", rangeHDR) // Try with range request
				res, err = http.DefaultClient.Do(req)
				if err != nil {
					log.Error().Err(err).Msg("Failed to request range request")
					return
				}

				if res.StatusCode == http.StatusRequestedRangeNotSatisfiable && res.ContentLength == 0 {
					break
				}

				if res.StatusCode != http.StatusPartialContent {
					log.Error().Int("code", res.StatusCode).Msg("Expected partial content response")
					return
				}

			}
		}()

		written, err := streamWavRTP(reader, d.RTPWriter)
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
	wavBuf := bytes.NewReader(samples)
	written, err := streamWavRTP(wavBuf, d.RTPWriter)
	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	return err
}

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

func streamWavRTP(body io.Reader, rtpWriter *sipgox.RTPWriter) (int, error) {
	dec := aud.NewWavDecoderStreamer(body)
	if err := dec.ReadHeaders(); err != nil {
		return 0, err
	}
	if dec.BitsPerSample != 16 {
		return 0, fmt.Errorf("received bitdepth=%d, but only 16 bit PCM supported", dec.BitsPerSample)
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
