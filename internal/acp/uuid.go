package acp

import (
	"crypto/rand"
	"fmt"
	"time"
)

func newUUIDv7(now time.Time) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}
	milliseconds := uint64(now.UnixMilli())
	value[0], value[1], value[2] = byte(milliseconds>>40), byte(milliseconds>>32), byte(milliseconds>>24)
	value[3], value[4], value[5] = byte(milliseconds>>16), byte(milliseconds>>8), byte(milliseconds)
	value[6] = value[6]&0x0f | 0x70
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}
