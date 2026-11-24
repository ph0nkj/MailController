// Package mailer parses recipient lists and drives bulk-mailing campaigns.
package mailer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
)

const (
	// DefaultMaxRecipients caps one campaign import.
	DefaultMaxRecipients = 50_000
	// invalidSampleLimit is how many rejected values are reported back.
	invalidSampleLimit = 5
	// maxRawValueLength avoids echoing huge malformed fields.
	maxRawValueLength = 120
	// maxEmailLength is the practical RFC 5321 path limit.
	maxEmailLength = 254
	// maxLocalPartLength is the RFC 5321 local-part limit.
	maxLocalPartLength = 64
)

// ErrTooManyRecipients is returned when an import exceeds the configured cap.
var ErrTooManyRecipients = errors.New("mailer: too many recipients")

// ParseOptions controls a recipient import.
type ParseOptions struct {
	// MaxRecipients defaults to DefaultMaxRecipients.
	MaxRecipients int
}

// ParseStats summarizes a recipient import.
type ParseStats struct {
	// Records is the number of non-empty data records read.
	Records int
	// Accepted is the number of unique, valid addresses.
	Accepted int
	// Duplicates counts valid addresses seen more than once.
	Duplicates int
	// Invalid counts records that contained no valid address.
	Invalid int
	// InvalidSamples holds up to five short examples of rejected input.
	InvalidSamples []string
}

// ParseRecipients reads addresses from plain text or CSV and calls emit once
// for every unique valid address. Input is processed record by record, so large
// files do not need to be buffered in full.
//
// Supported formats:
//   - one address or "Name <address>" per line;
//   - comma, semicolon or tab separated values;
//   - optional header row with an "email" (or "e-mail"/"email address") column.
//
// Addresses are lower-cased and deduplicated. Invalid values are counted and
// sampled, not returned as errors; an emit or CSV read error aborts the import.
func ParseRecipients(r io.Reader, opts ParseOptions, emit func(email string) error) (ParseStats, error) {
	if r == nil {
		return ParseStats{}, errors.New("mailer: recipient reader is nil")
	}
	if emit == nil {
		return ParseStats{}, errors.New("mailer: recipient callback is nil")
	}
	maxRecipients := opts.MaxRecipients
	if maxRecipients <= 0 || maxRecipients > DefaultMaxRecipients {
		maxRecipients = DefaultMaxRecipients
	}

	reader := bufio.NewReaderSize(r, 64*1024)
	csvReader := csv.NewReader(reader)
	csvReader.Comma = detectDelimiter(reader)
	csvReader.FieldsPerRecord = -1
	csvReader.LazyQuotes = true
	csvReader.TrimLeadingSpace = true
	csvReader.ReuseRecord = true

	var (
		stats     ParseStats
		seen      = make(map[string]struct{})
		headerCol = -1
		firstRow  = true
	)
	for {
		record, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("mailer: parse recipients: %w", err)
		}
		if firstRow {
			firstRow = false
			if index, ok := emailHeaderIndex(record); ok {
				headerCol = index
				continue
			}
		}
		if isBlankRecord(record) {
			continue
		}
		stats.Records++

		email, ok := extractEmail(record, headerCol)
		if !ok {
			stats.Invalid++
			if len(stats.InvalidSamples) < invalidSampleLimit {
				stats.InvalidSamples = append(stats.InvalidSamples, sampleRecord(record))
			}
			continue
		}
		if _, exists := seen[email]; exists {
			stats.Duplicates++
			continue
		}
		if stats.Accepted >= maxRecipients {
			return stats, fmt.Errorf("%w: limit is %d", ErrTooManyRecipients, maxRecipients)
		}
		seen[email] = struct{}{}
		stats.Accepted++
		if err := emit(email); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// Render substitutes the only supported template placeholder. Validated email
// addresses cannot contain HTML or header metacharacters, so the result is safe
// for both text and HTML bodies.
func Render(template, email string) string {
	return strings.ReplaceAll(template, "{{email}}", email)
}

func detectDelimiter(reader *bufio.Reader) rune {
	line, err := reader.Peek(4096)
	if len(line) == 0 {
		_ = err
		return ','
	}
	if index := bytes.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	hasComma := bytes.ContainsRune(line, ',')
	hasSemicolon := bytes.ContainsRune(line, ';')
	hasTab := bytes.ContainsRune(line, '\t')
	switch {
	case hasComma:
		return ','
	case hasSemicolon:
		return ';'
	case hasTab:
		return '\t'
	default:
		return ','
	}
}

func emailHeaderIndex(record []string) (int, bool) {
	for index, field := range record {
		normalized := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(field, "\ufeff")))
		normalized = strings.Join(strings.Fields(normalized), " ")
		switch normalized {
		case "email", "e-mail", "email address", "e-mail address", "mail":
			return index, true
		}
	}
	return 0, false
}

func isBlankRecord(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func extractEmail(record []string, headerCol int) (string, bool) {
	if headerCol >= 0 {
		if headerCol >= len(record) {
			return "", false
		}
		return normalizeAddress(record[headerCol])
	}
	for _, field := range record {
		if email, ok := normalizeAddress(field); ok {
			return email, true
		}
	}
	return "", false
}

func normalizeAddress(raw string) (string, bool) {
	value := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	value = strings.TrimPrefix(value, "mailto:")
	if value == "" || len(value) > maxEmailLength+64 {
		return "", false
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil {
		// Some pasted lists contain bare addresses with surrounding punctuation.
		trimmed := strings.Trim(value, " \t,;<>\"'")
		if trimmed == value {
			return "", false
		}
		parsed, err = mail.ParseAddress(trimmed)
		if err != nil {
			return "", false
		}
	}
	email := strings.ToLower(strings.TrimSpace(parsed.Address))
	if !validEmail(email) {
		return "", false
	}
	return email, true
}

func validEmail(email string) bool {
	if len(email) == 0 || len(email) > maxEmailLength {
		return false
	}
	if strings.Count(email, "@") != 1 {
		return false
	}
	at := strings.IndexByte(email, '@')
	local, domain := email[:at], email[at+1:]
	if local == "" || len(local) > maxLocalPartLength {
		return false
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	for _, r := range local {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`()<>[]\;:,`, r) {
			return false
		}
	}
	if domain == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= '0' && r <= '9':
			case r == '-':
			default:
				return false
			}
		}
	}
	return true
}

func sampleRecord(record []string) string {
	sample := strings.Join(record, ",")
	sample = strings.TrimSpace(sample)
	if len(sample) > maxRawValueLength {
		sample = sample[:maxRawValueLength] + "…"
	}
	return sample
}
