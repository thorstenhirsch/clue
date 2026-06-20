package main

import (
	"machine"
)

const maxTokenLen = 2048

// readToken reads the bearer token from flash.
// Returns empty string if no valid token is stored.
func readToken() string {
	if machine.Flash.Size() < 4 {
		return ""
	}
	header := make([]byte, 4)
	if _, err := machine.Flash.ReadAt(header, 0); err != nil {
		return ""
	}
	// Magic bytes 'T' 'K' followed by big-endian length
	if header[0] != 'T' || header[1] != 'K' {
		return ""
	}
	length := int(header[2])<<8 | int(header[3])
	if length == 0 || length > maxTokenLen {
		return ""
	}
	buf := make([]byte, length)
	if _, err := machine.Flash.ReadAt(buf, 4); err != nil {
		return ""
	}
	return string(buf)
}

// writeToken writes the bearer token to flash.
func writeToken(token string) error {
	if len(token) > maxTokenLen {
		return errTokenTooLong
	}
	if err := machine.Flash.EraseBlocks(0, 1); err != nil {
		return err
	}
	header := []byte{'T', 'K', byte(len(token) >> 8), byte(len(token))}
	if _, err := machine.Flash.WriteAt(header, 0); err != nil {
		return err
	}
	if _, err := machine.Flash.WriteAt([]byte(token), 4); err != nil {
		return err
	}
	return nil
}

type tokenError struct{}

func (tokenError) Error() string { return "token too long" }

var errTokenTooLong = tokenError{}
