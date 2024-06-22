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
