package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed www/*
var RootDir embed.FS

// GetHTTPFileServer returns an HTTP file server for the embedded static files
func GetHTTPFileServer() (http.Handler, error) {
	// Create a sub-filesystem to serve just the www directory contents
	wwwFS, err := fs.Sub(RootDir, "www")
	if err != nil {
		return nil, err
	}
	
	return http.FileServer(http.FS(wwwFS)), nil
}

// MountHTTPHandler mounts the file server at the specified path prefix
func MountHTTPHandler(mux *http.ServeMux, pathPrefix string) error {
	fsHandler, err := GetHTTPFileServer()
	if err != nil {
		return err
	}
	
	// If path prefix provided, handle it
	if pathPrefix != "" && pathPrefix != "/" {
		// Strip the prefix from the request URL path
		fsHandler = http.StripPrefix(pathPrefix, fsHandler)
		
		// Register the handler
		mux.Handle(path.Join(pathPrefix, "/"), fsHandler)
	} else {
		// Register the handler at the root
		mux.Handle("/", fsHandler)
	}
	
	return nil
}
