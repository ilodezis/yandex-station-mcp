package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func varint(v uint64) []byte {
	var buf []byte
	for v >= 0x80 {
		buf = append(buf, byte((v&0x7f)|0x80))
		v >>= 7
	}
	buf = append(buf, byte(v&0x7f))
	return buf
}

func integerField(field uint64, val uint64) []byte {
	return append(varint((field<<3)|0), varint(val)...)
}

func bytesField(field uint64, data []byte) []byte {
	key := varint((field << 3) | 2)
	length := varint(uint64(len(data)))
	res := append(key, length...)
	return append(res, data...)
}

func rpcFrame(remoteObj string, methodField uint64, payload []byte) []byte {
	var remote []byte
	remote = append(remote, bytesField(7, []byte(remoteObj))...)
	remote = append(remote, bytesField(methodField, payload)...)

	var envelope []byte
	envelope = append(envelope, bytesField(252, remote)...)
	envelope = append(envelope, integerField(253, 210)...) // TYPE_REMOTING = 210

	length := uint32(len(envelope))
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, length)
	return append(buf, envelope...)
}

func directiveFrame(name string, jsonPayload string, rawPayload []byte) []byte {
	var d []byte
	d = append(d, bytesField(1, []byte(name))...)
	if len(rawPayload) > 0 {
		d = append(d, bytesField(25, rawPayload)...) // raw_payload = 25
	} else if jsonPayload != "" {
		d = append(d, bytesField(6, []byte(jsonPayload))...) // json_payload = 6
	} else {
		d = append(d, bytesField(6, []byte("{}"))...)
	}

	var envelope []byte
	envelope = append(envelope, bytesField(252, d)...)
	envelope = append(envelope, integerField(253, 211)...) // TYPE_DIRECTIVE = 211

	length := uint32(len(envelope))
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, length)
	return append(buf, envelope...)
}

func subscribeFrame() []byte {
	return rpcFrame("AliceCapability", 20, bytesField(1, []byte("AliceCapability")))
}

func vocalizeFrame(text string) []byte {
	var directive []byte
	directive = append(directive, bytesField(1, []byte(text))...)
	directive = append(directive, integerField(2, 1)...)

	var payload []byte
	payload = append(payload, integerField(1, 18)...) // START_VOCALIZE = 18
	payload = append(payload, integerField(3, 1)...)  // events = true
	payload = append(payload, bytesField(13, directive)...)

	return rpcFrame("AliceCapability", 8, payload)
}

func playbackFrame(methodVal uint64) []byte {
	var pcm []byte
	pcm = append(pcm, integerField(1, methodVal)...)
	return rpcFrame("PlaybackControlCapability", 13, pcm)
}

func dndFrame(enable bool) []byte {
	var val uint64 = 1 // ForceDisable
	if enable {
		val = 2 // ForceEnable
	}
	raw := integerField(1, val)
	return directiveFrame("set_do_not_disturb_directive", "", raw)
}

func parseFields(data []byte) map[uint64]interface{} {
	result := make(map[uint64]interface{})
	offset := 0
	readNumber := func() (uint64, error) {
		var val uint64
		var shift uint
		for {
			if offset >= len(data) {
				return 0, io.EOF
			}
			b := data[offset]
			offset++
			val |= uint64(b&0x7f) << shift
			if b < 0x80 {
				return val, nil
			}
			shift += 7
			if shift >= 64 {
				return 0, errors.New("varint overflow")
			}
		}
	}

	for offset < len(data) {
		key, err := readNumber()
		if err != nil {
			break
		}
		fieldNum := key >> 3
		wireType := key & 0x7
		switch wireType {
		case 0:
			v, err := readNumber()
			if err != nil {
				return result
			}
			result[fieldNum] = v
		case 2:
			length, err := readNumber()
			if err != nil {
				return result
			}
			if offset+int(length) > len(data) {
				return result
			}
			val := make([]byte, length)
			copy(val, data[offset:offset+int(length)])
			offset += int(length)
			result[fieldNum] = val
		case 1:
			if offset+8 > len(data) {
				return result
			}
			offset += 8
		case 5:
			if offset+4 > len(data) {
				return result
			}
			offset += 4
		default:
			return result
		}
	}
	return result
}

func dialSocket() (net.Conn, error) {
	return net.DialTimeout("tcp", "127.0.0.1:9875", 2*time.Second)
}

func sendAndDrain(frame []byte, waitTTS bool) error {
	conn, err := dialSocket()
	if err != nil {
		return fmt.Errorf("dial 127.0.0.1:9875: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if !waitTTS {
		// Wait shortly for any immediate response
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		return nil
	}

	started := false
	_ = conn.SetReadDeadline(time.Now().Add(25 * time.Second))
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return nil
			}
			return err
		}
		size := binary.LittleEndian.Uint32(header)
		if size == 0 || size > 512*1024 {
			return fmt.Errorf("invalid frame size: %d", size)
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return err
		}

		envelope := parseFields(payload)
		remoteBytes, ok := envelope[252].([]byte)
		if !ok {
			continue
		}
		remote := parseFields(remoteBytes)
		if f18Bytes, ok := remote[18].([]byte); ok {
			f18 := parseFields(f18Bytes)
			if event, ok := f18[1].(uint64); ok {
				if event == 6 {
					started = true
				} else if event == 1 && started {
					return nil
				}
			}
		}
	}
}

func handleSay(args []string) error {
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		return errors.New("text cannot be empty")
	}
	sub := subscribeFrame()
	voc := vocalizeFrame(text)
	return sendAndDrain(append(sub, voc...), true)
}

func handlePlayer(args []string) error {
	if len(args) == 0 {
		return errors.New("player command required (pause, play, toggle, next, prev)")
	}
	var code uint64
	switch strings.ToLower(args[0]) {
	case "play", "resume", "continue":
		code = 0
	case "pause", "stop":
		code = 1
	case "toggle":
		code = 2
	case "next":
		code = 6
	case "prev", "previous":
		code = 7
	default:
		return fmt.Errorf("unknown player command %q, allowed: pause, play, toggle, next, prev", args[0])
	}
	return sendAndDrain(playbackFrame(code), false)
}

func handleDND(args []string) error {
	if len(args) == 0 {
		return errors.New("dnd action required: enable or disable")
	}
	switch strings.ToLower(args[0]) {
	case "enable", "on", "1", "true":
		return sendAndDrain(dndFrame(true), false)
	case "disable", "off", "0", "false":
		return sendAndDrain(dndFrame(false), false)
	default:
		return fmt.Errorf("unknown dnd action %q, allowed: enable, disable", args[0])
	}
}

func handleDirective(args []string) error {
	if len(args) == 0 {
		return errors.New("directive name required")
	}
	name := args[0]
	payload := "{}"
	if len(args) > 1 {
		payload = args[1]
	}
	return sendAndDrain(directiveFrame(name, payload, nil), false)
}

func main() {
	prog := filepath.Base(os.Args[0])
	args := os.Args[1:]

	// If invoked as "say", behave like say unless a known command flag is used
	if strings.Contains(prog, "say") {
		if len(args) > 0 {
			switch args[0] {
			case "--player", "-player", "player":
				if err := handlePlayer(args[1:]); err != nil {
					fmt.Fprintf(os.Stderr, "player error: %v\n", err)
					os.Exit(1)
				}
				return
			case "--dnd", "-dnd", "dnd":
				if err := handleDND(args[1:]); err != nil {
					fmt.Fprintf(os.Stderr, "dnd error: %v\n", err)
					os.Exit(1)
				}
				return
			case "--directive", "-directive", "directive":
				if err := handleDirective(args[1:]); err != nil {
					fmt.Fprintf(os.Stderr, "directive error: %v\n", err)
					os.Exit(1)
				}
				return
			}
		}
		if err := handleSay(args); err != nil {
			fmt.Fprintf(os.Stderr, "say error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s <say|player|dnd|directive> [args...]\n", prog)
		os.Exit(1)
	}

	var err error
	switch strings.ToLower(args[0]) {
	case "say":
		err = handleSay(args[1:])
	case "player":
		err = handlePlayer(args[1:])
	case "dnd":
		err = handleDND(args[1:])
	case "directive":
		err = handleDirective(args[1:])
	default:
		err = fmt.Errorf("unknown subcommand %q, allowed: say, player, dnd, directive", args[0])
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
