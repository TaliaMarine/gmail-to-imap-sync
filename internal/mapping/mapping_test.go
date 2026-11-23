package mapping

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func catalog(labels ...Label) map[string]Label {
	m := map[string]Label{}
	for _, l := range labels {
		m[l.ID] = l
	}
	return m
}

func sys(id string) Label       { return Label{ID: id, Name: id, System: true} }
func usr(id, name string) Label { return Label{ID: id, Name: name} }

var baseCatalog = catalog(
	sys("INBOX"), sys("SENT"), sys("DRAFT"), sys("SPAM"), sys("TRASH"),
	sys("STARRED"), sys("UNREAD"), sys("IMPORTANT"), sys("CATEGORY_PROMOTIONS"),
	usr("L1", "Projects"), usr("L2", "Finance"), usr("L3", "Newsletters"),
	usr("L4", "Newsletters/Weekly"), usr("L5", "Work/Customers"), usr("L6", "Newsletters2"),
)

func TestDecideFolders(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   []string
		skip   bool
	}{
		{"spec example: multi label + inbox", []string{"L1", "L2", "INBOX"}, []string{"INBOX", "Finance", "Projects"}, false},
		{"hierarchical label", []string{"L5"}, []string{"Work/Customers"}, false},
		{"inbox only", []string{"INBOX", "UNREAD"}, []string{"INBOX"}, false},
		{"sent goes to sent folder, not archive", []string{"SENT"}, []string{"Sent"}, false},
		{"sent also in inbox (mail to self)", []string{"SENT", "INBOX"}, []string{"INBOX", "Sent"}, false},
		{"archived and unlabelled goes to archive", nil, []string{"Archive"}, false},
		{"only non-folder system labels go to archive", []string{"IMPORTANT", "CATEGORY_PROMOTIONS", "STARRED"}, []string{"Archive"}, false},
		{"category does not hide the inbox copy", []string{"INBOX", "CATEGORY_PROMOTIONS"}, []string{"INBOX"}, false},
		// D4: an excluded label removes only its own folder.
		{"excluded label drops only its own folder", []string{"L2", "L3"}, []string{"Finance"}, false},
		{"all labels excluded is skipped, never archived", []string{"L3"}, nil, true},
		{"excluded plus non-folder labels is skipped", []string{"L3", "IMPORTANT"}, nil, true},
		{"spam is skipped", []string{"SPAM"}, nil, true},
		{"trash with inbox keeps inbox copy", []string{"TRASH", "INBOX"}, []string{"INBOX"}, false},
		{"draft excluded by default", []string{"DRAFT"}, nil, true},
		{"unknown label is ignored", []string{"GONE", "L1"}, []string{"Projects"}, false},
	}
	s := DefaultSettings()
	s.Excluded = append(s.Excluded, Matcher{Kind: MatchPrefix, Value: "Newsletters"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Decide(tc.labels, baseCatalog, s)
			if !reflect.DeepEqual(p.Folders, tc.want) && !(len(p.Folders) == 0 && len(tc.want) == 0) {
				t.Errorf("folders = %v, want %v", p.Folders, tc.want)
			}
			if p.Skip != tc.skip {
				t.Errorf("skip = %v, want %v", p.Skip, tc.skip)
			}
			if p.Skip && p.SkipReason != SkipAllExcluded {
				t.Errorf("skip reason = %q", p.SkipReason)
			}
		})
	}
}

func TestPrefixDoesNotMatchSiblingWithSharedStem(t *testing.T) {
	s := DefaultSettings()
	s.Excluded = []Matcher{{Kind: MatchPrefix, Value: "Newsletters/"}}
	// "Newsletters" itself and "Newsletters/Weekly" are excluded, "Newsletters2" is not.
	for id, wantSkip := range map[string]bool{"L3": true, "L4": true, "L6": false} {
		p := Decide([]string{id}, baseCatalog, s)
		if p.Skip != wantSkip {
			t.Errorf("label %s skip = %v, want %v", id, p.Skip, wantSkip)
		}
	}
}

func TestMatcher(t *testing.T) {
	l := Label{ID: "Label_12", Name: "Work/Customers"}
	cases := []struct {
		m    Matcher
		want bool
	}{
		{Matcher{Kind: MatchID, Value: "Label_12"}, true},
		{Matcher{Kind: MatchID, Value: "label_12", CaseInsensitive: true}, false}, // IDs never fold
		{Matcher{Kind: MatchName, Value: "Work/Customers"}, true},
		{Matcher{Kind: MatchName, Value: "work/customers"}, false},
		{Matcher{Kind: MatchName, Value: "work/customers", CaseInsensitive: true}, true},
		{Matcher{Kind: MatchPrefix, Value: "Work"}, true},
		{Matcher{Kind: MatchPrefix, Value: "Work/"}, true},
		{Matcher{Kind: MatchPrefix, Value: "Wor"}, false},
		{Matcher{Kind: MatchPrefix, Value: "WORK", CaseInsensitive: true}, true},
		{Matcher{Kind: MatchPrefix, Value: "WORK"}, false},
		{Matcher{Kind: MatchPrefix, Value: "Work/Customers"}, true},
		{Matcher{Kind: MatchPrefix, Value: ""}, false},
		{Matcher{Kind: MatchPrefix, Value: "/"}, false},
		{Matcher{Kind: "regex", Value: ".*"}, false},
	}
	for _, tc := range cases {
		if got := tc.m.Matches(l); got != tc.want {
			t.Errorf("%+v matches = %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestExclusionsCanBeOverridden(t *testing.T) {
	s := DefaultSettings()
	s.Excluded = nil // the administrator removed the defaults
	p := Decide([]string{"SPAM"}, baseCatalog, s)
	if p.Skip || !reflect.DeepEqual(p.Folders, []string{"Spam"}) {
		t.Errorf("spam with no exclusions = %+v", p)
	}
	p = Decide([]string{"DRAFT"}, baseCatalog, s)
	if !reflect.DeepEqual(p.Folders, []string{"Drafts"}) || !reflect.DeepEqual(p.Flags, []string{FlagSeen, FlagDraft}) {
		t.Errorf("drafts = %+v", p)
	}
}

func TestExcludeByName(t *testing.T) {
	s := DefaultSettings()
	s.Excluded = []Matcher{{Kind: MatchName, Value: "finance", CaseInsensitive: true}}
	p := Decide([]string{"L2", "L1"}, baseCatalog, s)
	if !reflect.DeepEqual(p.Folders, []string{"Projects"}) {
		t.Errorf("folders = %v", p.Folders)
	}
}

func TestLabelMappingAndConfiguredFolders(t *testing.T) {
	s := DefaultSettings()
	s.InboxFolder, s.SentFolder, s.ArchiveFolder = "INBOX", "Sent Items", "All Mail"
	s.LabelMapping = map[string]string{"L1": "Clients/Projects", "L2": "Money"}
	p := Decide([]string{"L1", "L2", "SENT"}, baseCatalog, s)
	want := []string{"Clients/Projects", "Money", "Sent Items"}
	if !reflect.DeepEqual(p.Folders, want) {
		t.Errorf("folders = %v, want %v", p.Folders, want)
	}
	if p := Decide(nil, baseCatalog, s); !reflect.DeepEqual(p.Folders, []string{"All Mail"}) {
		t.Errorf("archive folder = %v", p.Folders)
	}
}

func TestMappingCanRedirectSystemLabel(t *testing.T) {
	s := DefaultSettings()
	s.LabelMapping = map[string]string{"INBOX": "Mail/Inbox"}
	if p := Decide([]string{"INBOX"}, baseCatalog, s); !reflect.DeepEqual(p.Folders, []string{"Mail/Inbox"}) {
		t.Errorf("folders = %v", p.Folders)
	}
}

func TestFolderLabelsOptIn(t *testing.T) {
	s := DefaultSettings()
	s.FolderLabels = []string{"STARRED", "IMPORTANT", "CATEGORY_PROMOTIONS"}
	p := Decide([]string{"STARRED", "IMPORTANT", "CATEGORY_PROMOTIONS", "UNREAD"}, baseCatalog, s)
	want := []string{"Important", "Promotions", "Starred"}
	if !reflect.DeepEqual(p.Folders, want) {
		t.Errorf("folders = %v, want %v", p.Folders, want)
	}
}

func TestFlags(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		mut    func(*Settings)
		want   []string
	}{
		{"read message", []string{"INBOX"}, nil, []string{FlagSeen}},
		{"unread message", []string{"INBOX", "UNREAD"}, nil, nil},
		{"starred read", []string{"INBOX", "STARRED"}, nil, []string{FlagSeen, FlagFlagged}},
		{"starred unread", []string{"INBOX", "STARRED", "UNREAD"}, nil, []string{FlagFlagged}},
		{"flags off", []string{"INBOX", "STARRED"}, func(s *Settings) { s.UnreadAsSeen, s.StarredAsFlagged = false, false }, nil},
	}
	for _, tc := range cases {
		s := DefaultSettings()
		if tc.mut != nil {
			tc.mut(&s)
		}
		if got := Decide(tc.labels, baseCatalog, s).Flags; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: flags = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUnknownLabelsAreReported(t *testing.T) {
	p := Decide([]string{"Z9", "L1", "A1"}, baseCatalog, DefaultSettings())
	if !reflect.DeepEqual(p.Unknown, []string{"A1", "Z9"}) {
		t.Errorf("unknown = %v", p.Unknown)
	}
	// Only unknown labels: not skipped, goes to archive, caller refreshes and re-plans.
	p = Decide([]string{"Z9"}, baseCatalog, DefaultSettings())
	if p.Skip || !reflect.DeepEqual(p.Folders, []string{"Archive"}) {
		t.Errorf("plan = %+v", p)
	}
}

func TestDecideIsDeterministicAndDeduplicates(t *testing.T) {
	c := catalog(usr("a", "Work"), usr("b", "work"), usr("c", "WORK"), sys("INBOX"))
	s := DefaultSettings()
	first := Decide([]string{"a", "b", "c", "INBOX"}, c, s)
	if len(first.Folders) != 2 || first.Folders[0] != "INBOX" {
		t.Fatalf("folders = %v", first.Folders)
	}
	// Whatever the order, the same label set yields the same plan.
	perms := [][]string{{"c", "INBOX", "b", "a"}, {"INBOX", "c", "a", "b"}}
	for _, ids := range perms {
		got := Decide(ids, c, s)
		if len(got.Folders) != 2 || got.Folders[0] != "INBOX" || !strings.EqualFold(got.Folders[1], "work") {
			t.Errorf("order %v: folders = %v", ids, got.Folders)
		}
	}
	// Exact same input order gives exactly the same output.
	if again := Decide([]string{"a", "b", "c", "INBOX"}, c, s); !reflect.DeepEqual(first, again) {
		t.Error("not deterministic")
	}
}

func TestDelimiterTranslation(t *testing.T) {
	s := DefaultSettings()
	s.Delimiter = "."
	c := catalog(usr("a", "Work/Customers"), usr("b", "v1.2 notes"))
	p := Decide([]string{"a", "b"}, c, s)
	want := []string{"Work.Customers", "v1_2 notes"} // a literal "." in a name must not create hierarchy
	if !reflect.DeepEqual(p.Folders, want) {
		t.Errorf("folders = %v, want %v", p.Folders, want)
	}
}

func TestFolderName(t *testing.T) {
	long := strings.Repeat("é", 150)
	cases := []struct {
		in, delim string
		want      string
		ok        bool
	}{
		{"Projects", "/", "Projects", true},
		{"Work/Customers", "/", "Work/Customers", true},
		{"  Work / Customers  ", "/", "Work/Customers", true},
		{"Work//Customers", "/", "Work/Customers", true},
		{"/Work/", "/", "Work", true},
		{"a*b%c\\d", "/", "a_b_c_d", true},
		{"tab\there\x00", "/", "tabhere", true},
		{"a.b", ".", "a_b", true},
		{"a/b", ".", "a.b", true},
		{"", "/", "", false},
		{"   ", "/", "", false},
		{"///", "/", "", false},
		{"Café/日本語", "/", "Café/日本語", true},
		{long, "/", strings.Repeat("é", maxSegmentRunes), true},
		{"1/2/3/4/5/6/7/8/9/10", "/", "1/2/3/4/5/6/7/8_9_10", true},
	}
	for _, tc := range cases {
		got, ok := FolderName(tc.in, tc.delim)
		if got != tc.want || ok != tc.ok {
			t.Errorf("FolderName(%q, %q) = %q, %v; want %q, %v", tc.in, tc.delim, got, ok, tc.want, tc.ok)
		}
	}
}

func FuzzFolderName(f *testing.F) {
	for _, s := range []string{"", "a/b", "*%", "  /  ", "é/日本", "a\x00b", "../..", strings.Repeat("a/", 20)} {
		f.Add(s, "/")
		f.Add(s, ".")
	}
	f.Fuzz(func(t *testing.T, raw, delim string) {
		if len([]rune(delim)) != 1 || !utf8.ValidString(delim) || unicode.IsSpace([]rune(delim)[0]) || strings.ContainsAny(delim, "*%\\_") {
			t.Skip()
		}
		got, ok := FolderName(raw, delim)
		if !ok {
			if got != "" {
				t.Fatalf("not ok but %q", got)
			}
			return
		}
		if !utf8.ValidString(got) && utf8.ValidString(raw) {
			t.Fatalf("invalid UTF-8 from valid input: %q", got)
		}
		if strings.ContainsAny(got, "*%\\") {
			t.Fatalf("forbidden character in %q", got)
		}
		segs := strings.Split(got, delim)
		if len(segs) > maxDepth {
			t.Fatalf("too deep: %q", got)
		}
		for _, s := range segs {
			if s == "" || s != strings.TrimSpace(s) || len([]rune(s)) > maxSegmentRunes {
				t.Fatalf("bad segment %q in %q", s, got)
			}
			for _, r := range s {
				if unicode.IsControl(r) {
					t.Fatalf("control character in %q", got)
				}
			}
		}
		// Idempotent: cleaning a result under the same delimiter changes nothing,
		// as long as the delimiter is not "/" (then "/" would be re-split, which is the same).
		if again, ok2 := FolderName(got, delim); delim == "/" && (!ok2 || again != got) {
			t.Fatalf("not idempotent: %q -> %q", got, again)
		}
	})
}

func TestPreviewFlagsCollisionsAndExclusions(t *testing.T) {
	labels := []Label{sys("INBOX"), sys("SPAM"), usr("a", "Work"), usr("b", "work"), usr("c", "Solo"), sys("STARRED")}
	got := Preview(labels, DefaultSettings())
	by := map[string]LabelPreview{}
	for _, p := range got {
		by[p.LabelID] = p
	}
	if !by["SPAM"].Excluded || by["SPAM"].Folder != "" {
		t.Errorf("SPAM = %+v", by["SPAM"])
	}
	if by["STARRED"].Folder != "" || by["STARRED"].Excluded {
		t.Errorf("STARRED = %+v", by["STARRED"])
	}
	if !reflect.DeepEqual(by["a"].CollidesWith, []string{"b"}) || !reflect.DeepEqual(by["b"].CollidesWith, []string{"a"}) {
		t.Errorf("collisions: a=%v b=%v", by["a"].CollidesWith, by["b"].CollidesWith)
	}
	if len(by["c"].CollidesWith) != 0 || by["c"].Folder != "Solo" {
		t.Errorf("Solo = %+v", by["c"])
	}
	// System labels first, then user labels alphabetically.
	if !got[0].System || got[len(got)-1].Name != "work" {
		t.Errorf("order: %+v", got)
	}
}

func TestPreviewMappingCollisionWithSystemFolder(t *testing.T) {
	s := DefaultSettings()
	s.LabelMapping = map[string]string{"a": "inbox"} // collides with INBOX case-insensitively
	got := Preview([]Label{sys("INBOX"), usr("a", "Mail")}, s)
	for _, p := range got {
		if len(p.CollidesWith) != 1 {
			t.Errorf("%s collides with %v", p.LabelID, p.CollidesWith)
		}
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	bad := DefaultSettings()
	bad.ArchiveFolder = " / "
	bad.Excluded = []Matcher{{Kind: "regex", Value: "x"}, {Kind: MatchName, Value: ""}}
	bad.LabelMapping = map[string]string{"L1": "///"}
	bad.Delimiter = "ab"
	if (Settings{Delimiter: "_"}).Validate() == nil {
		t.Error("underscore delimiter accepted")
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("invalid settings accepted")
	}
	for _, frag := range []string{"archive folder", "regex", "empty value", "mapping for label", "delimiter"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("error %q lacks %q", err, frag)
		}
	}
}
