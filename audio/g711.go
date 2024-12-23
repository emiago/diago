// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"io"

	"github.com/zaf/g711"
)

func EncodeUlawTo(ulaw []byte, lpcm []byte) (n int, err error) {
	if len(lpcm) > len(ulaw)*2 {
		return 0, io.ErrShortBuffer
	}

	for i, j := 0, 0; j <= len(lpcm)-2; i, j = i+1, j+2 {
		ulaw[i] = g711.EncodeUlawFrame(int16(lpcm[j]) | int16(lpcm[j+1])<<8)
		n++
	}
	return n, nil
}

func DecodeUlawTo(lpcm []byte, ulaw []byte) (n int, err error) {
	if ulaw == nil {
		return 0, nil
	}

	if len(lpcm) < 2*len(ulaw) {
		return 0, io.ErrShortBuffer
	}
	for i, j := 0, 0; i < len(ulaw); i, j = i+1, j+2 {
		frame := g711.DecodeUlawFrame(ulaw[i])
		lpcm[j] = byte(frame)
		lpcm[j+1] = byte(frame >> 8)
		n += 2
	}
	return n, nil
}

func EncodeAlawTo(alaw []byte, lpcm []byte) (n int, err error) {
	if len(lpcm) > len(alaw)*2 {
		return 0, io.ErrShortBuffer
	}

	for i, j := 0, 0; j <= len(lpcm)-2; i, j = i+1, j+2 {
		alaw[i] = g711.EncodeAlawFrame(int16(lpcm[j]) | int16(lpcm[j+1])<<8)
		n++
	}
	return n, nil
}

func DecodeAlawTo(lpcm []byte, alaw []byte) (n int, err error) {
	if alaw == nil {
		return 0, nil
	}

	if len(lpcm) < len(alaw)*2 {
		return 0, io.ErrShortBuffer
	}
	for i, j := 0, 0; i < len(alaw); i, j = i+1, j+2 {
		frame := g711.DecodeAlawFrame(alaw[i])
		lpcm[j] = byte(frame)
		lpcm[j+1] = byte(frame >> 8)
		n += 2
	}
	return n, nil
}
