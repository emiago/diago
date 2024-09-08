// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package sdp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

var bufReader = sync.Pool{
	New: func() interface{} {
		// The Pool's New function should generally only return pointer
		// types, since a pointer can be put into the return interface
		// value without an allocation:
		return new(bytes.Buffer)
	},
}

type SessionDescription map[string][]string

func (sd SessionDescription) Values(key string) []string {
	return sd[key]
}

func (sd SessionDescription) Value(key string) string {
	values := sd[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// MediaDescription represents a media type.
// m=<media> <port>/<number of ports> <proto> <fmt> ...
// https://tools.ietf.org/html/rfc4566#section-5.14
type MediaDescription struct {
	MediaType string

	Port        int
	PortNumbers int

	Proto string

	Formats []string
}

func (m *MediaDescription) String() string {
	ports := strconv.Itoa(m.Port)
	if m.PortNumbers > 0 {
		ports += "/" + strconv.Itoa(m.PortNumbers)
	}

	return fmt.Sprintf("m=%s %s %s %s", m.MediaType, ports, m.Proto, strings.Join(m.Formats, " "))
}

func (sd SessionDescription) MediaDescription(mediaType string) (MediaDescription, error) {
	values := sd.Values("m")

	md := MediaDescription{}
	var v string
	for _, val := range values {
		ind := strings.Index(val, " ")
		if ind < 1 {
			continue
		}
		media := val[:ind]
		if media == mediaType {
			v = val
			break
		}
	}

	if v == "" {
		return md, fmt.Errorf("Media not found for %q", mediaType)
	}

	fields := strings.Fields(v)
	// TODO: is this really a must
	if len(fields) < 4 {
		return md, fmt.Errorf("Not enough fields in media description")
	}

	md.MediaType = fields[0]

	ports := strings.Split(fields[1], "/")
	md.Port, _ = strconv.Atoi(ports[0])
	if len(ports) > 1 {
		md.PortNumbers, _ = strconv.Atoi(ports[1])
	}

	md.Proto = fields[2]

	md.Formats = fields[3:]
	return md, nil
}

// c=<nettype> <addrtype> <connection-address>
// https://tools.ietf.org/html/rfc4566#section-5.7
type ConnectionInformation struct {
	NetworkType string
	AddressType string
	IP          net.IP
	TTL         int
	Range       int
}

func (sd SessionDescription) ConnectionInformation() (ci ConnectionInformation, err error) {
	v := sd.Value("c")
	if v == "" {
		return ci, fmt.Errorf("Connection information does not exists")
	}
	fields := strings.Fields(v)
	ci.NetworkType = fields[0]
	ci.AddressType = fields[1]
	addr := strings.Split(fields[2], "/")
	ci.IP = net.ParseIP(addr[0])

	switch ci.AddressType {
	case "IP4":
		ci.IP = ci.IP.To4()
		if ci.IP == nil {
			return ci, fmt.Errorf("failed to convert to IP4")
		}
	case "IP6":
		ci.IP = ci.IP.To16()
		if ci.IP == nil {
			return ci, fmt.Errorf("failed to convert to IP4")
		}
	}

	if len(addr) > 1 {
		ci.TTL, _ = strconv.Atoi(addr[1])
	}

	if len(addr) > 2 {
		ci.Range, _ = strconv.Atoi(addr[2])
	}
	return ci, nil
}

// Unmarshal is non validate version of sdp parsing
// Validation of values needs to be seperate
// NOT OPTIMIZED
func Unmarshal(data []byte, sdptr *SessionDescription) error {
	reader := bufReader.Get().(*bytes.Buffer)
	defer bufReader.Put(reader)
	reader.Reset()
	reader.Write(data)

	sd := *sdptr
	for {
		line, err := nextLine(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if len(line) < 2 {
			continue
		}

		ind := strings.Index(line, "=")
		if ind < 1 {
			return fmt.Errorf("Not a type=value line found. line=%q", line)
		}
		key := line[:ind]
		value := line[ind+1:]

		sd[key] = append(sd[key], value)
	}

}

func nextLine(reader *bytes.Buffer) (line string, err error) {
	// Scan full line without buffer
	// If we need to continue then try to grow
	line, err = reader.ReadString('\n')
	if err != nil {
		// We may get io.EOF and line till it was read
		return line, err
	}

	lenline := len(line)

	// Be tolerant for CRLF
	if line[lenline-2] == '\r' {
		return line[:lenline-2], nil
	}

	line = line[:lenline-1]
	return line, nil
}
