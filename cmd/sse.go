package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// sseEvent is one server-sent event.
type sseEvent struct {
	Event string
	Data  string
	ID    string
}

// sseReader reads server-sent events. It works on bytes and splits only at line ends, so a
// Persian character whose bytes arrive in two network reads is never cut in half; and it has
// no line-length cap (bufio.Scanner stops at 64 KB, which a long tool call can pass).
type sseReader struct {
	r *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{r: bufio.NewReaderSize(r, 32<<10)}
}

// next returns the next event with data, or io.EOF when the stream ends.
func (s *sseReader) next() (sseEvent, error) {
	var ev sseEvent
	var data bytes.Buffer
	hasData := false
	for {
		line, err := s.r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if hasData && errors.Is(err, io.EOF) {
				ev.Data = data.String()
				return ev, nil
			}
			return sseEvent{}, err
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if hasData {
				ev.Data = data.String()
				return ev, nil
			}
			ev = sseEvent{}
			if err != nil {
				return sseEvent{}, err
			}
			continue
		}
		if line[0] == ':' { // a comment, such as a keep-alive
			continue
		}
		field, value := line, []byte(nil)
		if i := bytes.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], line[i+1:]
			value = bytes.TrimPrefix(value, []byte(" "))
		}
		switch string(field) {
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.Write(value)
			hasData = true
		case "event":
			ev.Event = string(value)
		case "id":
			ev.ID = string(value)
		}
		if err != nil {
			// The stream ended without a blank line after the last field.
			if hasData {
				ev.Data = data.String()
				return ev, nil
			}
			return sseEvent{}, err
		}
	}
}
