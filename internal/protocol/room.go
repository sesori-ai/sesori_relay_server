package protocol

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

var roomCodeRegex = regexp.MustCompile(`^[0-9a-f]{4}-[0-9a-f]{4}$`)

// GenerateRoomCode generates a 4-character hex room code in format XXXX-XXXX
func GenerateRoomCode() (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	return fmt.Sprintf("%02x%02x-%02x%02x", bytes[0], bytes[1], bytes[2], bytes[3]), nil
}

// ValidateRoomCode validates a room code against the expected format
func ValidateRoomCode(code string) bool {
	return roomCodeRegex.MatchString(code)
}
