// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package testdata

import (
	"embed"
	"io/fs"
	"path"
)

//go:embed files/*.wav
var filesDir embed.FS

func OpenFile(filename string) (fs.File, error) {
	return filesDir.Open(path.Join("files", filename))
}

func ReadFile(filename string) ([]byte, error) {
	return filesDir.ReadFile(path.Join("files", filename))
}
