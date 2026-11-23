// Package mapping decides, for one Gmail message, which IMAP folders receive a
// copy and which flags it carries. It is a pure function of the message's
// label IDs, the account's label catalog and its settings: no I/O, so every
// rule can be unit tested (FULL_SPECS.md §5).
//
// Rules, in short:
//   - every non-excluded label that produces a folder gets a copy;
//   - an excluded label only removes its own folder (D4);
//   - if no folder remains because of exclusions, the message is skipped;
//   - if no folder remains and nothing was excluded (archived, unlabelled),
//     the message goes to the archive folder;
//   - STARRED/UNREAD drive flags, IMPORTANT and CATEGORY_* produce nothing
//     unless the settings opt them in as folders.
package mapping

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Gmail system label IDs the rules know about.
const (
	LabelInbox   = "INBOX"
	LabelSent    = "SENT"
	LabelDraft   = "DRAFT"
	LabelSpam    = "SPAM"
	LabelTrash   = "TRASH"
	LabelStarred = "STARRED"
	LabelUnread  = "UNREAD"
)

// IMAP flags the plan can set.
const (
	FlagSeen    = `\Seen`
	FlagFlagged = `\Flagged`
	FlagDraft   = `\Draft`
)

// Limits for generated folder names.
const (
	maxSegmentRunes = 100
	maxDepth        = 8
)

// Label is one entry of the account's Gmail label catalog.
type Label struct {
	ID     string
	Name   string
	System bool
}

// MatcherKind says what a Matcher compares against.
type MatcherKind string

const (
	MatchID     MatcherKind = "id"     // exact label ID
	MatchName   MatcherKind = "name"   // exact label name
	MatchPrefix MatcherKind = "prefix" // the label, or a label below it in the hierarchy
)

// Matcher is one exclusion rule.
type Matcher struct {
	Kind            MatcherKind `json:"kind"`
	Value           string      `json:"value"`
	CaseInsensitive bool        `json:"case_insensitive,omitempty"`
}

// Matches reports whether the matcher selects label l.
//
// A prefix matcher "Newsletters" (or "Newsletters/") selects the label
// "Newsletters" and every label below it ("Newsletters/Weekly"), but not
// "Newsletters2".
func (m Matcher) Matches(l Label) bool {
	switch m.Kind {
	case MatchID:
		return l.ID == m.Value // IDs are case sensitive
	case MatchName:
		return m.equal(l.Name, m.Value)
	case MatchPrefix:
		v := strings.TrimSuffix(m.Value, "/")
		if v == "" {
			return false
		}
		return m.equal(l.Name, v) || m.hasPrefix(l.Name, v+"/")
	}
	return false
}

func (m Matcher) equal(a, b string) bool {
	if m.CaseInsensitive {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (m Matcher) hasPrefix(s, prefix string) bool {
	if m.CaseInsensitive {
		return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
	}
	return strings.HasPrefix(s, prefix)
}

// DefaultExclusions are the labels excluded unless the administrator
// overrides them: spam, trash and drafts.
func DefaultExclusions() []Matcher {
	return []Matcher{
		{Kind: MatchID, Value: LabelSpam},
		{Kind: MatchID, Value: LabelTrash},
		{Kind: MatchID, Value: LabelDraft},
	}
}

// Settings are the per-account inputs to Plan.
type Settings struct {
	InboxFolder   string // default "INBOX"
	SentFolder    string // default "Sent"
	ArchiveFolder string // default "Archive"

	Excluded []Matcher

	// LabelMapping overrides the folder of a label, keyed by label ID. The
	// value uses "/" as hierarchy separator, whatever the server uses.
	LabelMapping map[string]string

	// FolderLabels lists system labels that also get a folder of their own
	// (for example STARRED or IMPORTANT). They are ignored by default.
	FolderLabels []string

	UnreadAsSeen     bool // a message without UNREAD is flagged \Seen
	StarredAsFlagged bool // STARRED becomes \Flagged

	// Delimiter is the destination server's hierarchy delimiter ("/" or ".").
	// Empty means "/".
	Delimiter string
}

// DefaultSettings returns the settings a new account starts with.
func DefaultSettings() Settings {
	return Settings{
		InboxFolder:      "INBOX",
		SentFolder:       "Sent",
		ArchiveFolder:    "Archive",
		Excluded:         DefaultExclusions(),
		UnreadAsSeen:     true,
		StarredAsFlagged: true,
		Delimiter:        "/",
	}
}

// Validate reports settings that cannot work.
func (s Settings) Validate() error {
	var errs []error
	for name, f := range map[string]string{"inbox": s.InboxFolder, "sent": s.SentFolder, "archive": s.ArchiveFolder} {
		if _, ok := FolderName(f, s.delimiter()); !ok {
			errs = append(errs, fmt.Errorf("%s folder %q is not a usable folder name", name, f))
		}
	}
	for _, m := range s.Excluded {
		switch m.Kind {
		case MatchID, MatchName, MatchPrefix:
		default:
			errs = append(errs, fmt.Errorf("exclusion kind %q is not one of id, name, prefix", m.Kind))
			continue
		}
		if strings.TrimSpace(m.Value) == "" {
			errs = append(errs, fmt.Errorf("exclusion of kind %q has an empty value", m.Kind))
		}
	}
	for id, f := range s.LabelMapping {
		if _, ok := FolderName(f, s.delimiter()); !ok {
			errs = append(errs, fmt.Errorf("mapping for label %q: %q is not a usable folder name", id, f))
		}
	}
	if d := s.Delimiter; d != "" && (len([]rune(d)) != 1 || strings.ContainsAny(d, "*%\\_") || unicode.IsSpace([]rune(d)[0])) {
		errs = append(errs, errors.New("delimiter must be a single character other than whitespace, *, %, _ or backslash"))
	}
	return errors.Join(errs...)
}

func (s Settings) delimiter() string {
	if s.Delimiter == "" {
		return "/"
	}
	return s.Delimiter
}

// SkipReason explains why a message is not copied.
type SkipReason string

const SkipAllExcluded SkipReason = "all_labels_excluded"

// Plan is the delivery decision for one message.
type Plan struct {
	// Folders receive a copy, deterministic order: the inbox first, then the
	// rest alphabetically. Never contains two names that differ only by case.
	Folders []string
	// Flags are set on every copy.
	Flags []string
	// Skip is true when the message must not be copied at all.
	Skip       bool
	SkipReason SkipReason
	// Unknown lists label IDs absent from the catalog. The caller should
	// refresh the catalog from Gmail and plan again.
	Unknown []string
}

// systemFolders names the folders of system labels that have one when they
// are not excluded.
var systemFolders = map[string]string{
	LabelSpam:  "Spam",
	LabelTrash: "Trash",
	LabelDraft: "Drafts",
}

// Decide plans delivery of a message carrying labelIDs. catalog maps label ID
// to Label.
func Decide(labelIDs []string, catalog map[string]Label, s Settings) Plan {
	var (
		p        Plan
		folders  = map[string]string{} // lower-cased name -> name
		excluded bool
		has      = map[string]bool{}
		delim    = s.delimiter()
	)
	folderLabels := map[string]bool{}
	for _, id := range s.FolderLabels {
		folderLabels[id] = true
	}

	for _, id := range labelIDs {
		has[id] = true
		l, ok := catalog[id]
		if !ok {
			p.Unknown = append(p.Unknown, id)
			continue
		}
		if s.excludes(l) {
			excluded = true
			continue
		}
		raw, ok := s.folderFor(l, folderLabels)
		if !ok {
			continue
		}
		name, ok := FolderName(raw, delim)
		if !ok {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := folders[key]; !dup {
			folders[key] = name
		}
	}

	switch {
	case len(folders) > 0:
	case excluded:
		p.Skip, p.SkipReason = true, SkipAllExcluded
	default:
		if name, ok := FolderName(s.ArchiveFolder, delim); ok {
			folders[strings.ToLower(name)] = name
		}
	}
	p.Folders = sortFolders(folders, s, delim)
	sort.Strings(p.Unknown)

	if s.UnreadAsSeen && !has[LabelUnread] {
		p.Flags = append(p.Flags, FlagSeen)
	}
	if s.StarredAsFlagged && has[LabelStarred] {
		p.Flags = append(p.Flags, FlagFlagged)
	}
	if has[LabelDraft] {
		p.Flags = append(p.Flags, FlagDraft)
	}
	return p
}

func (s Settings) excludes(l Label) bool {
	for _, m := range s.Excluded {
		if m.Matches(l) {
			return true
		}
	}
	return false
}

// folderFor returns the raw (unsanitised) folder name a label maps to.
func (s Settings) folderFor(l Label, folderLabels map[string]bool) (string, bool) {
	if f, ok := s.LabelMapping[l.ID]; ok {
		return f, true
	}
	switch l.ID {
	case LabelInbox:
		return s.InboxFolder, true
	case LabelSent:
		return s.SentFolder, true
	}
	if f, ok := systemFolders[l.ID]; ok {
		return f, true
	}
	if l.System {
		if !folderLabels[l.ID] {
			return "", false // STARRED, UNREAD, IMPORTANT, CATEGORY_*, CHAT...
		}
		return systemLabelTitle(l), true
	}
	return l.Name, true
}

// systemLabelTitle turns IMPORTANT into "Important" and CATEGORY_PROMOTIONS
// into "Promotions".
func systemLabelTitle(l Label) string {
	n := strings.TrimPrefix(l.ID, "CATEGORY_")
	if n == "" {
		return l.Name
	}
	return strings.ToUpper(n[:1]) + strings.ToLower(n[1:])
}

func sortFolders(m map[string]string, s Settings, delim string) []string {
	if len(m) == 0 {
		return nil
	}
	inbox, _ := FolderName(s.InboxFolder, delim)
	out := make([]string, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		ii, ij := strings.EqualFold(out[i], inbox), strings.EqualFold(out[j], inbox)
		if ii != ij {
			return ii
		}
		return out[i] < out[j]
	})
	return out
}

// FolderName converts a Gmail-style name ("Work/Customers", "/" separated)
// into a destination folder name using the server's hierarchy delimiter.
//
// Each segment is trimmed, stripped of control characters and of characters
// IMAP forbids in CREATE (the wildcards * and %, backslash), has any literal
// delimiter replaced by "_", and is truncated. Empty segments are dropped;
// hierarchies deeper than the limit are folded into the last allowed
// segment. ok is false when nothing usable remains.
func FolderName(raw, delim string) (string, bool) {
	if delim == "" {
		delim = "/"
	}
	var segs []string
	for _, seg := range strings.Split(raw, "/") {
		seg = cleanSegment(seg, delim)
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		return "", false
	}
	if len(segs) > maxDepth {
		tail := strings.Join(segs[maxDepth-1:], "_")
		segs = append(segs[:maxDepth-1:maxDepth-1], truncateRunes(tail, maxSegmentRunes))
	}
	return strings.Join(segs, delim), true
}

func cleanSegment(seg, delim string) string {
	var b strings.Builder
	for _, r := range seg {
		switch {
		case unicode.IsControl(r):
			continue
		case r == '*' || r == '%' || r == '\\':
			b.WriteRune('_')
		case string(r) == delim:
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return truncateRunes(strings.TrimSpace(b.String()), maxSegmentRunes)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// LabelPreview describes what one label does under the given settings.
type LabelPreview struct {
	LabelID  string
	Name     string
	System   bool
	Excluded bool
	Folder   string // empty when the label produces no folder
	// CollidesWith lists other label IDs that map to the same folder
	// (compared case-insensitively).
	CollidesWith []string
}

// Preview shows, for every label in the catalog, whether it is excluded and
// which folder it maps to, and flags labels that collide on one folder. The
// UI renders it before settings are saved.
func Preview(labels []Label, s Settings) []LabelPreview {
	folderLabels := map[string]bool{}
	for _, id := range s.FolderLabels {
		folderLabels[id] = true
	}
	out := make([]LabelPreview, 0, len(labels))
	byFolder := map[string][]int{}
	for _, l := range labels {
		p := LabelPreview{LabelID: l.ID, Name: l.Name, System: l.System, Excluded: s.excludes(l)}
		if !p.Excluded {
			if raw, ok := s.folderFor(l, folderLabels); ok {
				if name, ok := FolderName(raw, s.delimiter()); ok {
					p.Folder = name
					key := strings.ToLower(name)
					byFolder[key] = append(byFolder[key], len(out))
				}
			}
		}
		out = append(out, p)
	}
	for _, idx := range byFolder {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			for _, j := range idx {
				if i != j {
					out[i].CollidesWith = append(out[i].CollidesWith, out[j].LabelID)
				}
			}
			sort.Strings(out[i].CollidesWith)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].System != out[j].System {
			return out[i].System
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
