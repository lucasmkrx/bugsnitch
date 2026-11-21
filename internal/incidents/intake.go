package incidents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
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
	uuid          = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	eventID       = regexp.MustCompile(`(?i)^[a-f0-9]{32}$`)
	safeLabel     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,79}$`)
	errorClass    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.]{0,60}(Error|Exception|Panic)$`)
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

func signedBody(w http.ResponseWriter, r *http.Request, secret, header, prefix string) ([]byte, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", 405)
		return nil, false
	}
	if secret == "" {
		http.Error(w, "integration unavailable", 503)
		return nil, false
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request exceeds limit or could not be read", 413)
		return nil, false
	}
	sig, ok := strings.CutPrefix(r.Header.Get(header), prefix)
	got, err := hex.DecodeString(sig)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	if !ok || err != nil || !hmac.Equal(mac.Sum(nil), got) {
		http.Error(w, "invalid signature", 401)
		return nil, false
	}
	return b, true
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

func normalizeError(b []byte) (observation, error) {
	var p struct {
		Action       string
		Installation struct{ UUID string }
		Data         struct {
			Error struct {
				Project              json.Number
				EventID              string      `json:"event_id"`
				IssueID              json.Number `json:"issue_id"`
				Datetime             time.Time
				Environment, Release string
				Exception            struct {
					Values []struct {
						Type       string
						Stacktrace struct {
							Frames []struct {
								Filename string
								Lineno   int
								InApp    bool `json:"in_app"`
							}
						}
					}
				}
			}
		}
	}
	var o observation
	if json.Unmarshal(b, &p) != nil || p.Action != "created" || !uuid.MatchString(p.Installation.UUID) {
		return o, ErrInvalid
	}
	e := p.Data.Error
	project, e1 := e.Project.Int64()
	group, e2 := e.IssueID.Int64()
	if e1 != nil || e2 != nil || group <= 0 || project <= 0 || !eventID.MatchString(e.EventID) || e.Datetime.IsZero() || e.Datetime.After(time.Now().Add(5*time.Minute)) {
		return o, ErrInvalid
	}
	o = observation{Installation: strings.ToLower(p.Installation.UUID), ProviderProject: project, Event: strings.ToLower(e.EventID), Group: strconv.FormatInt(group, 10), Seen: e.Datetime,
		Observed: "Application error reported by Sentry", Environment: label(e.Environment), Release: label(e.Release), Frames: []Frame{}}
	scope := o.Installation + "/" + strconv.FormatInt(project, 10) + "/"
	o.Group = scope + o.Group
	o.Event = scope + o.Event
	if revision.MatchString(e.Release) {
		o.Release = e.Release
	}
	for _, exception := range e.Exception.Values {
		if errorClass.MatchString(exception.Type) && Redact(exception.Type) == exception.Type {
			o.Observed = exception.Type + " reported by Sentry"
		}
		for _, f := range exception.Stacktrace.Frames {
			if !f.InApp || f.Lineno <= 0 || len(o.Frames) >= 8 {
				continue
			}
			file, _, _ := strings.Cut(f.Filename, "?")
			file, _, _ = strings.Cut(file, "#")
			if name := label(path.Base(strings.ReplaceAll(file, "\\", "/"))); name != "" {
				o.Frames = append(o.Frames, Frame{name, f.Lineno})
			}
		}
	}
	return o, nil
}

func validText(s string) bool { return utf8.ValidString(s) && len(s) <= 4096 }
