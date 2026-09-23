package whatsapp

import (
	"strings"
	"unicode/utf8"
)

// Keep WhatsApp notifications comfortably below client/protocol limits while
// preserving complete Unicode characters. The platform accepts more, but a
// conservative ceiling avoids silent truncation in older clients.
const whatsappMaxTextRunes = 4000

func splitText(text string, max int) []string {
	if max < 1 || utf8.RuneCountInString(text) <= max {
		return []string{text}
	}

	const labelReserve = 32
	bodyMax := max - labelReserve
	if bodyMax < 1 {
		bodyMax = max
	}
	parts := splitTextRunes(text, bodyMax)
	for i := range parts {
		parts[i] = "[part " + itoa(i+1) + "/" + itoa(len(parts)) + "]\n" + parts[i]
	}
	return parts
}

func splitTextRunes(text string, max int) []string {
	runes := []rune(text)
	parts := make([]string, 0, (len(runes)/max)+1)
	for len(runes) > max {
		cut := max
		for i := max - 1; i > max/2; i-- {
			if runes[i] == '\n' || runes[i] == ' ' {
				cut = i + 1
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	if len(runes) > 0 {
		parts = append(parts, strings.TrimSpace(string(runes)))
	}
	return parts
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
