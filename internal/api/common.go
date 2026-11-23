package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// internalError logs err and returns an opaque 500: details stay in the log.
func (a *API) internalError(err error, what string) error {
	a.logger.Error(what, "error", err)
	return huma.Error500InternalServerError("internal error")
}

// notFoundOr maps "no rows" to 404 and anything else to a logged 500.
func (a *API) notFoundOr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return huma.Error404NotFound(what + " not found")
	}
	return a.internalError(err, "load "+what)
}

func parseID(s, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, huma.Error422UnprocessableEntity("invalid " + what + " id")
	}
	return id, nil
}

// pgCode returns the SQLSTATE of a PostgreSQL error, or "".
func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func isUniqueViolation(err error) bool { return pgCode(err) == "23505" }
func isFKViolation(err error) bool     { return pgCode(err) == "23503" }

// ts converts a nullable timestamp for JSON output.
func ts(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func tsOrZero(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func int8p(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullUUID(s string) (uuid.NullUUID, error) {
	if s == "" {
		return uuid.NullUUID{}, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.NullUUID{}, huma.Error422UnprocessableEntity("invalid id")
	}
	return uuid.NullUUID{UUID: id, Valid: true}, nil
}

func uuidPtr(n uuid.NullUUID) *string {
	if !n.Valid {
		return nil
	}
	s := n.UUID.String()
	return &s
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func tstz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()} }

// cursor is the keyset position of a paginated list: the last row returned.
type cursor struct {
	T  time.Time `json:"t"`
	ID uuid.UUID `json:"id"`
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	b, _ := json.Marshal(cursor{T: t.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, errors.New("invalid cursor")
	}
	if err := json.Unmarshal(b, &c); err != nil || c.T.IsZero() || c.ID == uuid.Nil {
		return cursor{}, errors.New("invalid cursor")
	}
	return c, nil
}

func pageSize(n int) int32 {
	switch {
	case n <= 0:
		return defaultPageSize
	case n > maxPageSize:
		return maxPageSize
	}
	return int32(n)
}

// shortError trims an error message for display, dropping anything past a
// reasonable length.
func shortError(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// queryRow runs a single-row query on the pool.
func (a *API) queryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return a.db.Pool.QueryRow(ctx, sql, args...)
}

// decodeObject decodes a JSON object column; anything else yields an empty map.
func decodeObject(raw []byte) map[string]any {
	m := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// decodeStrings decodes a JSON array of strings column.
func decodeStrings(raw []byte) []string {
	out := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}
