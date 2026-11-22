package incidents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid    = errors.New("invalid incident input")
	ErrConflict   = errors.New("incident or request has changed")
	ErrUnmapped   = errors.New("provider mapping unavailable")
	sensitiveLine = regexp.MustCompile("(?im)^.*(?:authorization|cookie|password|passwd|api[ _-]?key|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|secret[ _-]?key|bearer)\\s*[:= ]\\s*.+$")
	credentials   = regexp.MustCompile(`(?i)(?:https?://[^\s<>]+|(?:gh[pousr]_|github_pat_|sk-|xox[baprs]-)[a-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\b[a-z0-9+/_=-]{32,}\b)`)
	privateBlock  = regexp.MustCompile(`(?s)-----BEGIN [^-]+-----.*?(?:-----END [^-]+-----|$)`)
	requestKey    = regexp.MustCompile(`^[A-Za-z0-9_-]{8,100}$`)
	safeLabel     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,79}$`)
)

// Redact applies at ingestion and at issue publication. Untrusted markup never grants authority.
func Redact(s string) string {
	s = privateBlock.ReplaceAllString(s, "[redacted]")
	s = sensitiveLine.ReplaceAllString(s, "[redacted]")
	s = credentials.ReplaceAllString(s, "[redacted]")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, s))
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type Frame struct {
	File string `json:"file"`
	Line int    `json:"line"`
}
type observation struct {
	Installation                                                  string
	ProviderProject                                               int64
	Event, Group, Observed, Expected, Steps, Release, Environment string
	Seen                                                          time.Time
	Frames                                                        []Frame
}

func label(s string) string {
	if !safeLabel.MatchString(s) || Redact(s) != s {
		return ""
	}
	return s
}

func validText(s string) bool { return utf8.ValidString(s) && len(s) <= 4096 }
