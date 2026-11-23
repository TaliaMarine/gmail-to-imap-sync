package auth

import "strings"

// allowList decides who may sign in. Both lists empty means everyone the
// identity provider authenticates.
type allowList struct {
	emails  map[string]struct{} // lower-cased addresses
	domains []string            // lower-cased, including the leading "@"
	groups  map[string]struct{}
	open    bool
}

func newAllowList(emails, groups []string) allowList {
	l := allowList{emails: map[string]struct{}{}, groups: map[string]struct{}{}}
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case strings.HasPrefix(e, "@"):
			l.domains = append(l.domains, e)
		default:
			l.emails[e] = struct{}{}
		}
	}
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			l.groups[g] = struct{}{}
		}
	}
	l.open = len(l.emails) == 0 && len(l.domains) == 0 && len(l.groups) == 0
	return l
}

// Open reports whether the list lets everyone in.
func (l allowList) Open() bool { return l.open }

// permits reports whether the user may sign in. An address only counts when
// the provider says it is verified (or does not say anything).
func (l allowList) permits(email string, emailVerified bool, groups []string) bool {
	if l.open {
		return true
	}
	if email != "" && emailVerified {
		email = strings.ToLower(email)
		if _, ok := l.emails[email]; ok {
			return true
		}
		for _, d := range l.domains {
			if strings.HasSuffix(email, d) {
				return true
			}
		}
	}
	for _, g := range groups {
		if _, ok := l.groups[g]; ok {
			return true
		}
	}
	return false
}
