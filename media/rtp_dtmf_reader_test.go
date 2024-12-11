package media

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDTMFReader(t *testing.T) {
	r := RTPDtmfReader{}

	// DTMF 109
	sequence := []DTMFEvent{
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 0, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 0, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 0, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 0, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 0, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 0, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 0, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 9, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 9, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 9, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 9, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 9, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 9, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 9, EndOfEvent: true, Volume: 10, Duration: 800},
	}

	detected := strings.Builder{}
	for _, ev := range sequence {
		r.processDTMFEvent(ev)
		dtmf, set := r.ReadDTMF()
		if set {
			detected.WriteRune(dtmf)
		}
	}

	assert.Equal(t, "109", detected.String())
}

func TestDTMFReaderRepeated(t *testing.T) {
	r := RTPDtmfReader{}

	// DTMF 109
	sequence := []DTMFEvent{
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
	}

	detected := strings.Builder{}
	for _, ev := range sequence {
		r.processDTMFEvent(ev)
		dtmf, set := r.ReadDTMF()
		if set {
			detected.WriteRune(dtmf)
		}
	}

	assert.Equal(t, "111", detected.String())
}
