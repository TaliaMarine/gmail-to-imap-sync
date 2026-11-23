//go:build integration

package imapdest

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// dovecotConf is a minimal Dovecot 2.4 server: one virtual user, Maildir,
// plain IMAP on 143, "/" as hierarchy separator.
const dovecotConf = `dovecot_config_version = 2.4.0
dovecot_storage_version = 2.4.0

protocols = imap
log_path = /dev/stderr
ssl = no
auth_allow_cleartext = yes
auth_mechanisms = plain login

mail_driver = maildir
mail_home = /tmp/mail/%{user}
mail_path = ~/Maildir

mail_uid = vmail
mail_gid = vmail

# The image only has the vmail user and runs unprivileged.
base_dir = /tmp/run
state_dir = /tmp/run
default_login_user = vmail
default_internal_user = vmail
default_internal_group = vmail

namespace inbox {
  inbox = yes
  separator = /
}

passdb passwd-file {
  passwd_file_path = /etc/dovecot/users
  default_password_scheme = plain
}

userdb static {
  fields {
    uid = vmail
    gid = vmail
    home = /tmp/mail/%{user}
  }
}

service imap-login {
  inet_listener imap {
    port = 143
  }
}
`

func startDovecot(t *testing.T) Config {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "dovecot/dovecot:2.4.5",
			ExposedPorts: []string{"143/tcp"},
			Files: []testcontainers.ContainerFile{
				{Reader: stringsReader(dovecotConf), ContainerFilePath: "/etc/dovecot/dovecot.conf", FileMode: 0o644},
				{Reader: stringsReader("archive:{plain}secret\n"), ContainerFilePath: "/etc/dovecot/users", FileMode: 0o644},
			},
			WaitingFor: wait.ForListeningPort("143/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("dovecot container unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "143/tcp")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port.Port())
	return Config{Host: host, Port: p, TLSMode: TLSNone, Username: "archive", Password: "secret", OpTimeout: 30 * time.Second}
}

func TestDovecotDelivery(t *testing.T) {
	cfg := startDovecot(t)

	s, err := Open(ctx, Options{Config: cfg, Conns: 3, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("delimiter %q", s.Delimiter())

	m := msg("m1")
	m.Raw = []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: =?utf-8?q?caf=C3=A9?=\r\nMessage-ID: <m1@x>\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\nH\xe9llo, accents \xe9\xe8\r\n")
	rs := s.Deliver(ctx, m, []string{"INBOX", "Projects", "Work/Customers", "Archive"})
	for _, r := range rs {
		if r.Status != Delivered {
			t.Fatalf("result = %+v", r)
		}
	}

	// A new session (a restart) finds every earlier copy and skips it.
	s2, err := Open(ctx, Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for _, r := range s2.Deliver(ctx, m, []string{"INBOX", "Projects", "Work/Customers", "Archive"}) {
		if r.Status != Skipped || r.Created {
			t.Errorf("redelivery = %+v", r)
		}
	}

	// Another message in the same folders, concurrently.
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			s.Deliver(ctx, msg("c"+strconv.Itoa(i)), []string{"Projects", "Work/Customers"})
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}

	// Maildir++ refuses dots in folder names: the delivery must fall back, once, and stay findable.
	dotted := []string{"Project 1.0", "A Processer .../Newsletters"}
	for _, r := range s.Deliver(ctx, msg("dots"), dotted) {
		if r.Status != Delivered || r.Folder == r.Requested {
			t.Errorf("dotted folder: %+v", r)
		}
	}
	for _, r := range s2.Deliver(ctx, msg("dots"), dotted) {
		if r.Status != Skipped {
			t.Errorf("dotted folder after restart: %+v", r)
		}
	}
	t.Logf("renames: %v", s.Renames())

	// Read back: exact content, flags and date survived, and the index sees all.
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, folder := range []string{"Projects", "Work/Customers"} {
		idx, err := c.LoadIndex(ctx, folder, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(idx.IDs) != 21 {
			t.Errorf("%s: %d archive IDs, want 21", folder, len(idx.IDs))
		}
		if has, err := c.HasID(ctx, folder, "acct.m1"); err != nil || !has {
			t.Errorf("%s: HasID = %v, %v", folder, has, err)
		}
		if has, err := c.HasID(ctx, folder, "acct.m"); err != nil || has {
			t.Errorf("%s: prefix must not match: %v, %v", folder, has, err)
		}
	}
	// Byte fidelity, flags and INTERNALDATE as stored by Dovecot.
	if _, err := c.examine(ctx, "INBOX"); err != nil {
		t.Fatal(err)
	}
	bodySection := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{
		Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{bodySection},
	}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch = %v, %v", msgs, err)
	}
	wantRaw, _ := AddHeader(m.Raw, m.ID)
	if got := msgs[0].FindBodySection(bodySection); !bytes.Equal(got, wantRaw) {
		t.Errorf("stored bytes differ:\n got %q\nwant %q", got, wantRaw)
	}
	if !msgs[0].InternalDate.Equal(m.Date) {
		t.Errorf("INTERNALDATE = %v, want %v", msgs[0].InternalDate, m.Date)
	}
	var flags []imap.Flag
	for _, f := range msgs[0].Flags {
		if f != imap.Flag(`\Recent`) { // set by the server per session, not by us
			flags = append(flags, f)
		}
	}
	if len(flags) != 1 || flags[0] != imap.FlagSeen {
		t.Errorf("flags = %v", msgs[0].Flags)
	}
	names, _ := c.ListFolders(ctx)
	t.Logf("folders: %v", names)
}
