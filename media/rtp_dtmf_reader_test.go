package media

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func testDTMFLoopSequence(r *RTPDtmfReader, sequence []DTMFEvent) string {
	detected := strings.Builder{}
	for i, ev := range sequence {
		fmt.Println("Processing", ev, i, i%7 == 0)
		r.processDTMFEvent(ev, i%7 == 0)
		dtmf, set := r.ReadDTMF()
		if set {
			detected.WriteRune(dtmf)
		}
	}
	return detected.String()
}

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

	dtmf := testDTMFLoopSequence(&r, sequence)
	assert.Equal(t, "109", dtmf)
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

	dtmf := testDTMFLoopSequence(&r, sequence)
	assert.Equal(t, "111", dtmf)
}

func TestDTMFReaderLatePacket(t *testing.T) {
	r := RTPDtmfReader{}

	// DTMF 109
	sequence := []DTMFEvent{
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 160},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 320},
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 480},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800}, // End event received before
		{Event: 1, EndOfEvent: false, Volume: 10, Duration: 640},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 10, Duration: 800},
	}

	dtmf := testDTMFLoopSequence(&r, sequence)
	assert.Equal(t, "1", dtmf)
}

func TestDTMFReaderCases(t *testing.T) {
	t.Skip("We need to check can this be test valid")
	r := RTPDtmfReader{}

	// DTMF 109
	sequence := []DTMFEvent{
		{Event: 1, EndOfEvent: false, Volume: 0, Duration: 0},
		{Event: 1, EndOfEvent: false, Volume: 0, Duration: 0},
		{Event: 1, EndOfEvent: false, Volume: 0, Duration: 0},
		{Event: 1, EndOfEvent: false, Volume: 0, Duration: 0},
		{Event: 1, EndOfEvent: true, Volume: 0, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 0, Duration: 800},
		{Event: 1, EndOfEvent: true, Volume: 0, Duration: 800},
	}

	dtmf := testDTMFLoopSequence(&r, sequence)
	assert.Equal(t, "1", dtmf)
}
