// Package testenv starts the real services integration tests need (PostgreSQL
// and Dovecot) in containers. It is only imported by tests.
package testenv

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

// DB starts PostgreSQL, applies the migrations and returns a connected DB.
// The test is skipped when containers are unavailable.
func DB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("t"), postgres.WithUsername("t"),
		postgres.WithPassword("t"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("postgres container unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	return d
}

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

// Dovecot starts a Dovecot 2.4 IMAP server with the user archive/secret
// (Maildir, "/" separator, no TLS) and returns its connection settings.
func Dovecot(t *testing.T) imapdest.Config {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "dovecot/dovecot:2.4.5",
			ExposedPorts: []string{"143/tcp"},
			Files: []testcontainers.ContainerFile{
				{Reader: strings.NewReader(dovecotConf), ContainerFilePath: "/etc/dovecot/dovecot.conf", FileMode: 0o644},
				{Reader: strings.NewReader("archive:{plain}secret\n"), ContainerFilePath: "/etc/dovecot/users", FileMode: 0o644},
			},
			WaitingFor: wait.ForListeningPort("143/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("dovecot container unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "143/tcp")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port.Port())
	return imapdest.Config{Host: host, Port: p, TLSMode: imapdest.TLSNone, Username: "archive", Password: "secret", OpTimeout: 30 * time.Second}
}
