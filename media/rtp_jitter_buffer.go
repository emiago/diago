// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
)

const (
	defaultRTPJitterBufferDelayPackets = 3
	defaultRTPJitterBufferMaxPackets   = 10
)

var rtpJitterDebug = envBool("JITTER_DEBUG")

// RTPJitterBufferOptions configures a fixed RTP jitter buffer.
type RTPJitterBufferOptions struct {
	// DelayPackets is the initial fixed playout delay in packets. If unset, 3 is used.
	DelayPackets int
	// MaxPackets caps buffered packets and the forward reordering window. If unset, 10 is used.
	MaxPackets int
}

// RTPJitterBufferStatistics contains simple counters for buffer decisions.
type RTPJitterBufferStatistics struct {
	PacketsRead        uint64
	PacketsReleased    uint64
	PacketsLost        uint64
	PacketsLate        uint64
	PacketsDuplicate   uint64
	PacketsDropped     uint64
	SSRCResets         uint64
	LastSequenceNumber uint16
}

type rtpJitterBufferStats struct {
	packetsRead        atomic.Uint64
	packetsReleased    atomic.Uint64
	packetsLost        atomic.Uint64
	packetsLate        atomic.Uint64
	packetsDuplicate   atomic.Uint64
	packetsDropped     atomic.Uint64
	ssrcResets         atomic.Uint64
	lastSequenceNumber atomic.Uint32
}

type rtpJitterSlot struct {
	raw    []byte
	packet rtp.Packet
	n      int
	ssrc   uint32
	seq    uint16
}

type rtpJitterInput struct {
	slot int
	err  error
}

// RTPJitterBuffer is a fixed-delay, single-consumer RTPReader wrapper.
//
// It sits after RTPSession and before RTPPacketReader, so RTPSession observes
// true network arrival while downstream readers get reordered packets.
type RTPJitterBuffer struct {
	// reader is the upstream network-facing RTP source.
	reader RTPReader

	// packetDuration controls the interval between playout decisions.
	packetDuration time.Duration
	// delayPackets is the number of queued packets required to start playout early.
	delayPackets int
	// maxPackets is both the queue capacity and accepted forward sequence window.
	maxPackets int

	// input transfers ownership of filled slot indexes from readLoop to ReadRTP.
	input chan rtpJitterInput
	// freeSlots transfers ownership of reusable slot indexes back to readLoop.
	freeSlots chan int
	// done is closed by Close to stop internal channel operations and delivery.
	done chan struct{}
	// closeOnce makes Close idempotent.
	closeOnce sync.Once
	// startOnce ensures that only one upstream reader goroutine is launched.
	startOnce sync.Once

	// slots contains all reusable packet metadata and packet-sized byte regions.
	slots []rtpJitterSlot
	// sequence maps sequenceNumber % maxPackets to an occupied slot index, or -1.
	sequence []int
	// queued is the number of occupied entries in sequence.
	queued int

	// ssrc identifies the RTP source currently accepted by the buffer.
	ssrc uint32
	// ssrcSet reports whether the buffer has learned an RTP source.
	ssrcSet bool
	// expectedSeq is the next sequence number scheduled for playout.
	expectedSeq uint16
	// expectedSet reports whether expectedSeq has been initialized.
	expectedSet bool

	// initialTimer limits how long startup waits to collect delayPackets.
	initialTimer *time.Timer
	// playoutTimer schedules the next packet release or loss decision.
	playoutTimer *time.Timer
	// playout reports whether the initial buffering phase has completed.
	playout bool
	// releaseNow reports that the next expected sequence may be processed now.
	releaseNow bool

	// inputClosed reports that readLoop has stopped producing packet events.
	inputClosed bool
	// readErr stores the terminal upstream error returned after queued packets drain.
	readErr error
	// lastArrivalTime is used only for JITTER_DEBUG receive-side diagnostics.
	lastArrivalTime time.Time
	// lastArrivalSeq is the previous RTP sequence observed by readLoop.
	lastArrivalSeq uint16
	// lastArrivalSet reports whether receive-side debug arrival state is initialized.
	lastArrivalSet bool
	// stats uses atomics so Statistics may run concurrently with ReadRTP.
	stats rtpJitterBufferStats
}

// NewRTPJitterBuffer creates a fixed jitter buffer over another RTPReader.
// It panics if packetDuration is not greater than zero.
func NewRTPJitterBuffer(reader RTPReader, packetDuration time.Duration, opts RTPJitterBufferOptions) *RTPJitterBuffer {
	if packetDuration <= 0 {
		panic("media: RTP jitter buffer packetDuration must be greater than zero")
	}
	if opts.DelayPackets <= 0 {
		opts.DelayPackets = defaultRTPJitterBufferDelayPackets
	}
	if opts.MaxPackets <= 0 {
		opts.MaxPackets = defaultRTPJitterBufferMaxPackets
	}
	if opts.MaxPackets < opts.DelayPackets {
		opts.MaxPackets = opts.DelayPackets
	}
	if opts.DelayPackets > opts.MaxPackets {
		opts.DelayPackets = opts.MaxPackets
	}

	// One extra slot lets the network reader finish one read while the configured
	// jitter window is full. All packet storage is allocated once here.
	slotCount := opts.MaxPackets + 1
	data := make([]byte, slotCount*RTPBufSize)
	slots := make([]rtpJitterSlot, slotCount)
	freeSlots := make(chan int, slotCount)
	for i := range slots {
		slots[i].raw = data[i*RTPBufSize : (i+1)*RTPBufSize]
		freeSlots <- i
	}

	sequence := make([]int, opts.MaxPackets)
	for i := range sequence {
		sequence[i] = -1
	}

	initialTimer := time.NewTimer(time.Hour)
	initialTimer.Stop()
	playoutTimer := time.NewTimer(time.Hour)
	playoutTimer.Stop()

	return &RTPJitterBuffer{
		reader:         reader,
		packetDuration: packetDuration,
		delayPackets:   opts.DelayPackets,
		maxPackets:     opts.MaxPackets,
		input:          make(chan rtpJitterInput, slotCount),
		freeSlots:      freeSlots,
		done:           make(chan struct{}),
		slots:          slots,
		sequence:       sequence,
		initialTimer:   initialTimer,
		playoutTimer:   playoutTimer,
	}
}

// ReadRTP implements RTPReader. Calls must not overlap.
func (j *RTPJitterBuffer) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	j.start()

	for {
		if j.playout && j.releaseNow {
			position := int(j.expectedSeq) % j.maxPackets
			slotIndex := j.sequence[position]
			if slotIndex >= 0 && j.slots[slotIndex].seq == j.expectedSeq {
				slot := &j.slots[slotIndex]
				if len(buf) < slot.n {
					return 0, io.ErrShortBuffer
				}

				j.sequence[position] = -1
				j.queued--
				j.expectedSeq++
				j.releaseNow = false
				j.resetPlayoutTimer()

				n := copy(buf, slot.raw[:slot.n])
				err := RTPUnmarshal(buf[:n], p)
				j.recycleSlot(slotIndex)
				if err != nil {
					return 0, err
				}

				j.stats.packetsReleased.Add(1)
				j.stats.lastSequenceNumber.Store(uint32(p.SequenceNumber))
				return n, nil
			}

			j.stats.packetsLost.Add(1)
			j.expectedSeq++
			j.releaseNow = false
			j.resetPlayoutTimer()
		}

		if j.inputClosed && j.queued == 0 {
			j.stopTimers()
			if j.readErr != nil {
				return 0, j.readErr
			}
			return 0, io.EOF
		}

		var initialC <-chan time.Time
		if !j.playout && j.expectedSet {
			initialC = j.initialTimer.C
		}

		var playoutC <-chan time.Time
		if j.playout && !j.releaseNow {
			playoutC = j.playoutTimer.C
		}

		select {
		case <-j.done:
			j.stopTimers()
			return 0, io.ErrClosedPipe

		case input, ok := <-j.input:
			if !ok {
				j.inputClosed = true
				continue
			}
			if input.err != nil {
				j.readErr = input.err
				j.inputClosed = true
				continue
			}

			j.handleSlot(input.slot)
			if !j.playout && j.queued >= j.delayPackets {
				j.debugPlayoutStarted("delay_packets")
				j.startPlayout()
			}

		case <-initialC:
			j.debugPlayoutStarted("initial_timer")
			j.startPlayout()

		case <-playoutC:
			j.releaseNow = true
		}
	}
}

func (j *RTPJitterBuffer) start() {
	j.startOnce.Do(func() {
		go j.readLoop()
	})
}

func (j *RTPJitterBuffer) readLoop() {
	defer close(j.input)

	for {
		var slotIndex int
		select {
		case <-j.done:
			return
		case slotIndex = <-j.freeSlots:
		}

		slot := &j.slots[slotIndex]
		n, err := j.reader.ReadRTP(slot.raw, &slot.packet)
		if err != nil {
			j.sendInput(rtpJitterInput{err: err})
			return
		}
		if n == 0 {
			j.recycleSlot(slotIndex)
			continue
		}

		slot.n = n
		slot.ssrc = slot.packet.SSRC
		slot.seq = slot.packet.SequenceNumber
		j.debugArrival(slot)
		if !j.sendInput(rtpJitterInput{slot: slotIndex}) {
			return
		}
	}
}

func (j *RTPJitterBuffer) sendInput(input rtpJitterInput) bool {
	select {
	case <-j.done:
		return false
	case j.input <- input:
		return true
	}
}

func (j *RTPJitterBuffer) handleSlot(slotIndex int) {
	slot := &j.slots[slotIndex]
	j.stats.packetsRead.Add(1)

	if !j.ssrcSet || j.ssrc != slot.ssrc {
		if j.ssrcSet {
			j.stats.ssrcResets.Add(1)
		}
		j.resetStream(slot.ssrc, slot.seq)
	}

	distance := int16(slot.seq - j.expectedSeq)
	if distance < 0 {
		if j.playout {
			j.stats.packetsLate.Add(1)
			j.debugPacketDecision("late", slot, "behind_playout")
			j.recycleSlot(slotIndex)
			return
		}
		if !j.canMoveExpectedBack(slot.seq) {
			j.stats.packetsDropped.Add(1)
			j.debugPacketDecision("dropped", slot, "before_window")
			j.recycleSlot(slotIndex)
			return
		}
		j.expectedSeq = slot.seq
		distance = 0
	}

	if int(distance) >= j.maxPackets {
		j.stats.packetsDropped.Add(1)
		j.debugPacketDecision("dropped", slot, "beyond_window")
		j.recycleSlot(slotIndex)
		return
	}

	position := int(slot.seq) % j.maxPackets
	existing := j.sequence[position]
	if existing >= 0 {
		if j.slots[existing].seq == slot.seq {
			j.stats.packetsDuplicate.Add(1)
			j.debugPacketDecision("duplicate", slot, "same_sequence")
		} else {
			j.stats.packetsDropped.Add(1)
			j.debugPacketDecision("dropped", slot, "slot_collision")
		}
		j.recycleSlot(slotIndex)
		return
	}

	j.sequence[position] = slotIndex
	j.queued++
}

func (j *RTPJitterBuffer) canMoveExpectedBack(seq uint16) bool {
	for _, slotIndex := range j.sequence {
		if slotIndex < 0 {
			continue
		}
		distance := int16(j.slots[slotIndex].seq - seq)
		if distance < 0 || int(distance) >= j.maxPackets {
			return false
		}
	}
	return true
}

func (j *RTPJitterBuffer) resetStream(ssrc uint32, seq uint16) {
	j.clearSequence()
	j.ssrc = ssrc
	j.ssrcSet = true
	j.expectedSeq = seq
	j.expectedSet = true
	j.playout = false
	j.releaseNow = false
	j.stopAndDrainTimer(j.playoutTimer)
	j.resetTimer(j.initialTimer, time.Duration(j.delayPackets)*j.packetDuration)
}

func (j *RTPJitterBuffer) clearSequence() {
	for i, slotIndex := range j.sequence {
		if slotIndex < 0 {
			continue
		}
		j.sequence[i] = -1
		j.recycleSlot(slotIndex)
	}
	j.queued = 0
}

func (j *RTPJitterBuffer) startPlayout() {
	j.stopAndDrainTimer(j.initialTimer)
	j.playout = true
	j.releaseNow = true
}

func (j *RTPJitterBuffer) resetPlayoutTimer() {
	j.resetTimer(j.playoutTimer, j.packetDuration)
}

func (j *RTPJitterBuffer) resetTimer(timer *time.Timer, duration time.Duration) {
	j.stopAndDrainTimer(timer)
	timer.Reset(duration)
}

func (j *RTPJitterBuffer) stopAndDrainTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (j *RTPJitterBuffer) stopTimers() {
	j.stopAndDrainTimer(j.initialTimer)
	j.stopAndDrainTimer(j.playoutTimer)
}

func (j *RTPJitterBuffer) recycleSlot(slotIndex int) {
	select {
	case j.freeSlots <- slotIndex:
	case <-j.done:
	}
}

// Close stops jitter-buffer delivery. It does not close the injected reader.
func (j *RTPJitterBuffer) Close() error {
	j.closeOnce.Do(func() {
		close(j.done)
		j.stopTimers()
	})
	return nil
}

// Statistics returns a race-safe snapshot of jitter buffer counters.
func (j *RTPJitterBuffer) Statistics() RTPJitterBufferStatistics {
	return RTPJitterBufferStatistics{
		PacketsRead:        j.stats.packetsRead.Load(),
		PacketsReleased:    j.stats.packetsReleased.Load(),
		PacketsLost:        j.stats.packetsLost.Load(),
		PacketsLate:        j.stats.packetsLate.Load(),
		PacketsDuplicate:   j.stats.packetsDuplicate.Load(),
		PacketsDropped:     j.stats.packetsDropped.Load(),
		SSRCResets:         j.stats.ssrcResets.Load(),
		LastSequenceNumber: uint16(j.stats.lastSequenceNumber.Load()),
	}
}

func (j *RTPJitterBuffer) debugArrival(slot *rtpJitterSlot) {
	if !rtpJitterDebug {
		return
	}

	now := time.Now()
	if j.lastArrivalSet {
		arrivalDelta := now.Sub(j.lastArrivalTime)
		if arrivalDelta > j.packetDuration+j.packetDuration/2 {
			jitterDebugf("event=delayed_arrival seq=%d prev_seq=%d arrival_delta=%s packet_duration=%s",
				slot.seq,
				j.lastArrivalSeq,
				arrivalDelta,
				j.packetDuration,
			)
		}

		expectedNext := j.lastArrivalSeq + 1
		if slot.seq != expectedNext {
			jitterDebugf("event=out_of_sequence seq=%d prev_seq=%d expected_next=%d arrival_delta=%s",
				slot.seq,
				j.lastArrivalSeq,
				expectedNext,
				arrivalDelta,
			)
		}
	}

	j.lastArrivalTime = now
	j.lastArrivalSeq = slot.seq
	j.lastArrivalSet = true
}

func (j *RTPJitterBuffer) debugPacketDecision(event string, slot *rtpJitterSlot, reason string) {
	if !rtpJitterDebug {
		return
	}

	jitterDebugf("event=%s seq=%d expected_seq=%d queued=%d delay_packets=%d max_packets=%d reason=%s",
		event,
		slot.seq,
		j.expectedSeq,
		j.queued,
		j.delayPackets,
		j.maxPackets,
		reason,
	)
}

func (j *RTPJitterBuffer) debugPlayoutStarted(reason string) {
	if !rtpJitterDebug {
		return
	}

	jitterDebugf("event=playout_started queued=%d delay_packets=%d max_packets=%d reason=%s",
		j.queued,
		j.delayPackets,
		j.maxPackets,
		reason,
	)
}

func jitterDebugf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "JITTER_DEBUG "+format+"\n", args...)
}

func envBool(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

var _ RTPReader = (*RTPJitterBuffer)(nil)
var _ io.Closer = (*RTPJitterBuffer)(nil)
