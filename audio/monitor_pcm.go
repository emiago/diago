package audio

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/google/uuid"
)

var (
	RecordingFlushSize = 4096
)

type pcmBufioWriter struct {
	writer   *bufio.Writer // Lets use Buffered flushing
	mu       sync.Mutex
	stopped  bool
	lastTime time.Time
	codec    media.Codec
	silence  []byte
}

func (m *pcmBufioWriter) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Check do we need to inject silence as a tail
	// Case Reader had no stream running, but time passed
	if err := m.writeSilenceUnsafe(time.Now()); err != nil {
		return err
	}

	if err := m.writer.Flush(); err != nil {
		return err
	}

	return nil
}

func (m *pcmBufioWriter) writeSilenceUnsafe(now time.Time) error {
	diff := uint32(now.Sub(m.lastTime).Seconds() * float64(m.codec.SampleRate))
	srt := m.codec.SampleTimestamp()
	for i := 2 * srt; i < diff; i += srt {
		if _, err := m.writer.Write(m.silence); err != nil {
			return err
		}
	}
	m.lastTime = now
	return nil
}

func (m *pcmBufioWriter) writePCM(now time.Time, lpcm []byte) error {
	// We do not want to write on stopped monitoring
	// We need this, because user can stop monitoring, but still keep underhood stream active
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil
	}

	// Check do we need to inject first silence
	if err := m.writeSilenceUnsafe(now); err != nil {
		return err
	}

	_, err := m.writer.Write(lpcm)
	return err
}

func (m *pcmBufioWriter) Stop() {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
}

func (m *pcmBufioWriter) Start() {
	m.mu.Lock()
	m.stopped = false
	m.mu.Unlock()
}

// Monitoring starts with first packet arrived, but you can shift with start time. Ex stream are not continious
func (m *pcmBufioWriter) StartTime(t time.Time) {
	m.mu.Lock()
	m.lastTime = t
	m.mu.Unlock()
}

type MonitorPCMReader struct {
	pcmBufioWriter
	audioReader  io.Reader
	decoder      PCMDecoderBuffer
	FlushOnError bool
}

func (m *MonitorPCMReader) Init(w io.Writer, codec media.Codec, audioReader io.Reader) error {
	bw := bufio.NewWriterSize(w, RecordingFlushSize)
	m.writer = bw
	m.codec = codec
	m.audioReader = audioReader

	decoder := PCMDecoderBuffer{}
	if err := decoder.Init(codec); err != nil {
		return err
	}
	m.decoder = decoder

	samples16 := codec.Samples16()
	silence := bytes.Repeat([]byte{0}, samples16) // This alloc could be avoided
	m.silence = silence
	m.lastTime = time.Now()
	return nil
}

func (m *MonitorPCMReader) Read(b []byte) (int, error) {
	n, err := m.audioReader.Read(b)
	if err != nil {
		if m.FlushOnError {
			return n, errors.Join(err, m.Flush())
		}
		return n, err
	}
	now := time.Now()

	// Decode stream to PCM unless stream is already decoded?
	if _, err := m.decoder.Write(b[:n]); err != nil {
		return 0, err
	}
	lpcm := m.decoder.ReadFull()

	// Write to outer stream. Expecting some buffer with flushing will happen
	err = m.writePCM(now, lpcm)
	return n, err
}

type MonitorPCMWriter struct {
	pcmBufioWriter
	audioWriter  io.Writer
	decoder      PCMDecoderBuffer
	FlushOnError bool
}

func (m *MonitorPCMWriter) Init(w io.Writer, codec media.Codec, audioWriter io.Writer) error {
	bw := bufio.NewWriterSize(w, RecordingFlushSize)
	m.writer = bw
	m.codec = codec
	m.audioWriter = audioWriter

	decoder := PCMDecoderBuffer{}
	if err := decoder.Init(codec); err != nil {
		return err
	}
	m.decoder = decoder

	samples16 := codec.Samples16()
	silence := bytes.Repeat([]byte{0}, samples16) // This alloc could be avoided
	m.silence = silence
	m.lastTime = time.Now()
	return nil
}

func (m *MonitorPCMWriter) Write(b []byte) (int, error) {
	n, err := m.audioWriter.Write(b)
	if err != nil {
		if m.FlushOnError {
			return n, errors.Join(err, m.Flush())
		}
		return n, err
	}

	now := time.Now()
	// Decode stream to PCM unless stream is already decoded?
	if _, err := m.decoder.Write(b[:n]); err != nil {
		return 0, err
	}
	lpcm := m.decoder.ReadFull()

	// Write to outer stream. Expecting some buffer with flushing will happen
	err = m.writePCM(now, lpcm)
	return n, err
}

type MonitorPCMStereo struct {
	MonitorPCMReader
	MonitorPCMWriter

	PCMFileRead  *os.File
	PCMFileWrite *os.File

	recording io.Writer
}

// It supports only single codec, which must be same for reader and writer
func (m *MonitorPCMStereo) Init(record io.Writer, codec media.Codec, audioReader io.Reader, audioWriter io.Writer) error {
	m.recording = record

	uuid := uuid.New().String()
	var err error
	err = func() error {
		if m.PCMFileRead == nil {
			filepath := path.Join(os.TempDir(), uuid+"_monitor_reader.raw")
			m.PCMFileRead, err = os.OpenFile(filepath, os.O_CREATE|os.O_RDWR, 0755)
			if err != nil {
				return err
			}
		}

		if m.PCMFileWrite == nil {
			filepath := path.Join(os.TempDir(), uuid+"_monitor_writer.raw")
			m.PCMFileWrite, err = os.OpenFile(filepath, os.O_CREATE|os.O_RDWR, 0755)
			if err != nil {
				return err
			}
		}

		if err := m.MonitorPCMReader.Init(m.PCMFileRead, codec, audioReader); err != nil {
			return err
		}

		if err := m.MonitorPCMWriter.Init(m.PCMFileWrite, codec, audioWriter); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return errors.Join(err, m.removeTmpFiles())
	}

	return nil
}

func (m *MonitorPCMStereo) removeTmpFiles() (err error) {
	if m.PCMFileRead != nil {
		e1 := m.PCMFileRead.Close()
		e2 := os.Remove(m.PCMFileRead.Name())
		err = errors.Join(err, e1, e2)
	}

	if m.PCMFileWrite != nil {
		e1 := m.PCMFileWrite.Close()
		e2 := os.Remove(m.PCMFileWrite.Name())
		err = errors.Join(err, e1, e2)
	}
	return err
}

func (m *MonitorPCMStereo) Close() error {
	// Stop any current PCM writing
	m.MonitorPCMReader.Stop()
	m.MonitorPCMWriter.Stop()

	if err := m.Flush(); err != nil {
		return err
	}
	if err := m.interleave(); err != nil {
		return err
	}

	return m.removeTmpFiles()
}

func (m *MonitorPCMStereo) Flush() error {
	if err := m.MonitorPCMReader.Flush(); err != nil {
		return err
	}
	if err := m.MonitorPCMWriter.Flush(); err != nil {
		return err
	}
	return nil
}

func (m *MonitorPCMStereo) interleave() error {
	fr := m.PCMFileRead
	fw := m.PCMFileWrite
	recording := m.recording
	if _, err := fr.Seek(0, 0); err != nil {
		return err
	}
	if _, err := fw.Seek(0, 0); err != nil {
		return err
	}

	// Read frames from both files and interleave
	readBuf1 := make([]byte, RecordingFlushSize/2)
	readBuf2 := make([]byte, RecordingFlushSize/2)
	stereoBuf := make([]byte, (RecordingFlushSize/2)*2)
	size := 2 // 16 bit
	for {
		n1, err1 := io.ReadFull(fr, readBuf1)
		n2, err2 := io.ReadFull(fw, readBuf2)

		n := max(n1, n2)

		if (err1 != nil || err2 != nil) && n == 0 {
			if !errors.Is(err1, io.EOF) {
				return err1
			}

			if !errors.Is(err2, io.EOF) {
				return err2
			}
			break
		}
		// Shorter file ended, then pad its missing tail with silence
		clear(readBuf1[n1:n])
		clear(readBuf2[n2:n])

		// interleave
		copyN := 0
		for i, j := 0, 0; i < n; i += size {
			copyN += copy(stereoBuf[j:j+size], readBuf1[i:i+size])
			copyN += copy(stereoBuf[j+size:j+2*size], readBuf2[i:i+size])
			j += 2 * size // 2 channels * size
		}

		if _, err := recording.Write(stereoBuf[:copyN]); err != nil {
			return err
		}

	}

	return nil
}
