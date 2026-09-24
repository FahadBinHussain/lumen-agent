package whatsapp

import (
	"strings"
	"unicode/utf8"
)

// Keep WhatsApp notifications below the effective limit of the bridge/client
// path while preserving complete Unicode characters. Although WhatsApp's
// documented limit is larger, this conservative ceiling avoids truncation.
const whatsappMaxTextBytes = 900

func splitText(text string, max int) []string {
	if max < 1 || len(text) <= max {
		return []string{text}
	}

	const labelReserve = 32
	bodyMax := max - labelReserve
	if bodyMax < 1 {
		bodyMax = max
	}
	parts := splitTextBytes(text, bodyMax)
	for i := range parts {
		parts[i] = "[part " + itoa(i+1) + "/" + itoa(len(parts)) + "]\n" + parts[i]
	}
	return parts
}

func splitTextBytes(text string, max int) []string {
	runes := []rune(text)
	parts := make([]string, 0, (len(runes)/max)+1)
	for len(runes) > 0 {
		cut := 0
		bytes := 0
		for cut < len(runes) {
			n := utf8.RuneLen(runes[cut])
			if bytes+n > max {
				break
			}
			bytes += n
			cut++
		}
		if cut == len(runes) {
			parts = append(parts, strings.TrimSpace(string(runes)))
			break
		}
		for i := cut - 1; i > cut/2; i-- {
			if runes[i] == '\n' || runes[i] == ' ' {
				cut = i + 1
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
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
