package mailer

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func collectRecipients(t *testing.T, input string, opts ParseOptions) (ParseStats, []string) {
	t.Helper()
	var emails []string
	stats, err := ParseRecipients(strings.NewReader(input), opts, func(email string) error {
		emails = append(emails, email)
		return nil
	})
	require.NoError(t, err)
	return stats, emails
}

func TestParseRecipientsPlainLines(t *testing.T) {
	input := `
Alice <Alice@Example.COM>
bob@example.com
bob@example.com
not-an-email
a@b
c@example.org
mailto:dave+tag@example.net
`
	stats, emails := collectRecipients(t, input, ParseOptions{})
	require.Equal(t, []string{
		"alice@example.com",
		"bob@example.com",
		"c@example.org",
		"dave+tag@example.net",
	}, emails)
	require.Equal(t, 7, stats.Records)
	require.Equal(t, 4, stats.Accepted)
	require.Equal(t, 1, stats.Duplicates)
	require.Equal(t, 2, stats.Invalid)
	require.Contains(t, stats.InvalidSamples, "not-an-email")
	require.Contains(t, stats.InvalidSamples, "a@b")
}

func TestParseRecipientsCSVWithHeader(t *testing.T) {
	input := "Email,Name\nCarol@Example.com,Carol\nbad,Dave\n,Empty\nEve <eve@example.net>,Eve\nexample.com,No At\n"
	stats, emails := collectRecipients(t, input, ParseOptions{})
	require.Equal(t, []string{"carol@example.com", "eve@example.net"}, emails)
	require.Equal(t, 5, stats.Records)
	require.Equal(t, 2, stats.Accepted)
	require.Equal(t, 3, stats.Invalid)
	require.Len(t, stats.InvalidSamples, 3)
}

func TestParseRecipientsSemicolonAndBOM(t *testing.T) {
	input := "\ufeffemail;name\na@example.com;Alice\nb@example.org;Bob\n"
	stats, emails := collectRecipients(t, input, ParseOptions{})
	require.Equal(t, []string{"a@example.com", "b@example.org"}, emails)
	require.Equal(t, 2, stats.Accepted)
}

func TestParseRecipientsTabDelimitedWithoutHeader(t *testing.T) {
	stats, emails := collectRecipients(t, "a@example.com\tAlice\nb@example.org\tBob\n", ParseOptions{})
	require.Equal(t, []string{"a@example.com", "b@example.org"}, emails)
	require.Equal(t, 2, stats.Accepted)
}

func TestParseRecipientsHeaderOnly(t *testing.T) {
	stats, emails := collectRecipients(t, "Email\n", ParseOptions{})
	require.Empty(t, emails)
	require.Equal(t, 0, stats.Records)
}

func TestParseRecipientsEmptyInput(t *testing.T) {
	stats, emails := collectRecipients(t, " \n\n", ParseOptions{})
	require.Empty(t, emails)
	require.Equal(t, ParseStats{}, stats)
}

func TestParseRecipientsLimit(t *testing.T) {
	input := "a@example.com\nb@example.com\nc@example.com\n"
	var emitted []string
	stats, err := ParseRecipients(strings.NewReader(input), ParseOptions{MaxRecipients: 2}, func(email string) error {
		emitted = append(emitted, email)
		return nil
	})
	require.ErrorIs(t, err, ErrTooManyRecipients)
	require.Len(t, emitted, 2)
	require.Equal(t, 2, stats.Accepted)
}

func TestParseRecipientsEmitError(t *testing.T) {
	stop := errors.New("stop")
	stats, err := ParseRecipients(strings.NewReader("a@example.com\nb@example.com\n"), ParseOptions{}, func(string) error {
		return stop
	})
	require.ErrorIs(t, err, stop)
	require.Equal(t, 1, stats.Accepted)
}

func TestParseRecipientsRejectsNil(t *testing.T) {
	_, err := ParseRecipients(nil, ParseOptions{}, func(string) error { return nil })
	require.Error(t, err)
	_, err = ParseRecipients(strings.NewReader("a@example.com"), ParseOptions{}, nil)
	require.Error(t, err)
}

func TestValidEmail(t *testing.T) {
	valid := []string{
		"a@example.com",
		"user.name+tag@sub.example.co.uk",
		"user_name@example.io",
		"x@y.zz",
	}
	for _, email := range valid {
		require.True(t, validEmail(email), email)
	}
	invalid := []string{
		"",
		"plain",
		"a@b",
		"a@@example.com",
		"@example.com",
		"a@",
		"a..b@example.com",
		".a@example.com",
		"a.@example.com",
		"a b@example.com",
		"a@exa mple.com",
		"a@-example.com",
		"a@example-.com",
		"a@example..com",
		"a@.example.com",
		"a@example.com.",
		"user@[127.0.0.1]",
		strings.Repeat("a", 65) + "@example.com",
		"a@" + strings.Repeat("b", 250) + ".com",
	}
	for _, email := range invalid {
		require.False(t, validEmail(email), email)
	}
}

func TestNormalizeAddress(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{" Alice <Alice@Example.COM> ", "alice@example.com"},
		{"bob@example.com", "bob@example.com"},
		{"mailto:carol@example.org", "carol@example.org"},
	}
	for _, tt := range tests {
		got, ok := normalizeAddress(tt.input)
		require.True(t, ok, tt.input)
		require.Equal(t, tt.want, got)
	}

	for _, input := range []string{"", "no-at-sign", "Alice", "a@b"} {
		_, ok := normalizeAddress(input)
		require.False(t, ok, input)
	}
}

func TestRender(t *testing.T) {
	require.Equal(t, "Hello alice@example.com, welcome",
		Render("Hello {{email}}, welcome", "alice@example.com"))
	require.Equal(t, "no placeholders", Render("no placeholders", "a@example.com"))
}
