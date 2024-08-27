// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package sdp

import (
	"strconv"
	"strings"
)

const (
	FORMAT_TYPE_ULAW            = "0"
	FORMAT_TYPE_ALAW            = "8"
	FORMAT_TYPE_TELEPHONE_EVENT = "101"
)

type Formats []string

func NewFormats(fmts ...string) Formats {
	return Formats(fmts)
}

//	If the <proto> sub-field is "RTP/AVP" or "RTP/SAVP" the <fmt>//
//
// sub-fields contain RTP payload type numbers.
func (fmts Formats) ToNumeric() (nfmts []int, err error) {
	nfmt := make([]int, len(fmts))
	for i, f := range fmts {
		nfmt[i], err = strconv.Atoi(f)
		if err != nil {
			return
		}
	}
	return nfmt, nil
}

func (fmts Formats) String() string {
	out := make([]string, len(fmts))
	for i, v := range fmts {
		switch v {
		case "0":
			out[i] = "0(ulaw)"
		case "8":
			out[i] = "8(alaw)"
		default:
			// Unknown then just use as number
			out[i] = v
		}
	}
	return strings.Join(out, ",")
}

// Only valid for RTP/AVP formats
// For unknown it returns 0
func FormatNumeric(f string) uint8 {
	switch f {
	case FORMAT_TYPE_ALAW:
		return 8
	case FORMAT_TYPE_ULAW:
		return 0
	}
	return 0
}
