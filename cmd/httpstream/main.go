// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"runtime"
)

func main() {
	serveHTTP()
}

func serveHTTP() {
	_, file, _, _ := runtime.Caller(1)
	currentDir := path.Dir(file)
	aboveDir, _ := path.Split(currentDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(200)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		fh, err := os.Open(path.Join(aboveDir, "testdata", "demo-thanks.wav"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Get file info
		fileInfo, err := fh.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Add("content-type", "audio/wav")
		// w.Header().Add("cache-control", "max-age=10")
		// w.WriteHeader(http.StatusOK)
		fmt.Printf("Serving file %q", fh.Name())
		http.ServeContent(w, req, "audio/wav", fileInfo.ModTime(), fh)

		// _, err = io.Copy(w, fh)
		// if err != nil {
		// 	http.Error(w, err.Error(), http.StatusInternalServerError)
		// }
	})

	srv := http.Server{
		Addr:    "127.0.0.1:8080",
		Handler: mux,
	}

	l, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	defer l.Close()
	srv.Serve(l)
}
