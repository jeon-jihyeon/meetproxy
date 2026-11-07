package listen

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrEvent = errors.New("invalid listen event")

func Validate(source, id, kind string) error {
	if !valid(source, 256) || !valid(id, 256) || !valid(kind, 128) {
		return ErrEvent
	}
	return nil
}

func valid(value string, limit int) bool {
	if strings.TrimSpace(value) == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
