// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRTPExtendedSequenceNumberWrapping(t *testing.T) {
	var realSeq uint16 = (1<<16 - 1)
	seq := RTPExtendedSequenceNumber{
		seqNum: realSeq,
	}

	realSeq++
	seq.UpdateSeq(realSeq)

	assert.Equal(t, seq.wrapArroundCount, uint16(1))
	assert.Equal(t, seq.ReadExtendedSeq(), uint64(1<<16))
}
