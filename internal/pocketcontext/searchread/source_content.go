package searchread

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/pocketcontext/sqlread"
)

// SourceContentFields is the fixed allowlisted projection used by source search.
// It is also checked against nonhidden PocketBase base fields at startup.
func SourceContentFields() [][]string {
	return [][]string{
		{"spaces", "id"},
		{"sources", "id", "space", "title", "original_name", "source_date"},
		{"renditions", "id", "space", "source"},
		{"passages", "id", "space", "rendition", "body"},
	}
}
func validateSourceContent(i Index) error {
	if i.Collection != "sources" || !reflect.DeepEqual(i.Columns, []string{"title", "original_name", "source_date", "body"}) || i.SnippetColumn != "body" || i.Scope == nil || *i.Scope != (Scope{Collection: "spaces", Field: "id"}) || i.Where != "" || i.Members != "" {
		return fmt.Errorf("%w: source content requires the fixed evidence projection and space scope", ErrIndexInvalid)
	}
	return nil
}

type sourceContentReads struct{ spaces, sources, renditions, passages *sqlread.FilteredRead }

func prepareSourceContent(source *sqlread.SnapshotSource) (*sourceContentReads, error) {
	reads := &sourceContentReads{}
	fields := SourceContentFields()
	targets := []**sqlread.FilteredRead{&reads.spaces, &reads.sources, &reads.renditions, &reads.passages}
	selections := []string{"id", "space", "source", "rendition"}
	for n, field := range fields {
		read, err := source.PrepareFilteredRead(sqlread.FilteredReadSpec{Table: field[0], Columns: field[1:], SelectColumn: selections[n]})
		if err != nil {
			return nil, err
		}
		*targets[n] = read
	}
	return reads, nil
}
func sourceID(value any) (string, error) {
	id, ok := value.(string)
	if !ok || !validToken(id) || strings.Contains(id, ":") {
		return "", ErrIndexInvalid
	}
	return id, nil
}

// All relation reads retain their own requester filter and occur in one read
// transaction. Filtering by the selected space happens before snapshot limits,
// so unrelated readable spaces cannot alter ranks, generations or capacity.
func (e *PrivateEngine) authorizedSourceRows(ctx context.Context, requester string, d privateDefinition, scope string) ([]privateRow, error) {
	var rows []privateRow
	err := e.source.ReadFiltered(ctx, requester, func(tx *sqlread.FilteredTx) error {
		found := false
		if err := tx.Read(d.sourceContent.spaces, []string{scope}, func(v []any) error { found = true; return nil }); err != nil {
			return err
		}
		if !found {
			return ErrScopeNotFound
		}
		sources := map[string][]string{}
		size := 0
		appendRow := func(row privateRow) error {
			size += len(row.id)
			for _, value := range row.values {
				size += len(value)
			}
			if size > e.maxIndexBytes {
				return ErrIndexLimit
			}
			rows = append(rows, row)
			return nil
		}
		if err := tx.Read(d.sourceContent.sources, []string{scope}, func(v []any) error {
			id, err := sourceID(v[0])
			if err != nil {
				return err
			}
			if v[1] != scope || sources[id] != nil {
				return ErrIndexInvalid
			}
			values := make([]string, 4)
			for n, value := range v[2:] {
				if value == nil {
					continue
				}
				text, ok := value.(string)
				if !ok || !utf8.ValidString(text) {
					return ErrIndexInvalid
				}
				values[n] = text
			}
			sources[id] = values
			// A separate metadata row avoids inventing a passage for metadata-only hits.
			return appendRow(privateRow{id: id + ":", values: values})
		}); err != nil {
			return err
		}
		ids := make([]string, 0, len(sources))
		for id := range sources {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		renditions := map[string]string{}
		if err := tx.Read(d.sourceContent.renditions, ids, func(v []any) error {
			id, err := sourceID(v[0])
			if err != nil {
				return err
			}
			source, err := sourceID(v[2])
			if err != nil {
				return err
			}
			if v[1] != scope || sources[source] == nil || renditions[id] != "" {
				return ErrIndexInvalid
			}
			renditions[id] = source
			return nil
		}); err != nil {
			return err
		}
		ids = make([]string, 0, len(renditions))
		for id := range renditions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		seen := map[string]bool{}
		return tx.Read(d.sourceContent.passages, ids, func(v []any) error {
			id, err := sourceID(v[0])
			if err != nil {
				return err
			}
			rendition, err := sourceID(v[2])
			if err != nil {
				return err
			}
			source := renditions[rendition]
			body, ok := v[3].(string)
			if v[1] != scope || source == "" || seen[id] || !ok || !utf8.ValidString(body) {
				return ErrIndexInvalid
			}
			seen[id] = true
			values := append([]string(nil), sources[source]...)
			values[3] = body
			return appendRow(privateRow{id: source + ":" + id, values: values})
		})
	})
	switch {
	case err == nil:
	case errors.Is(err, sqlread.ErrSnapshotLimit):
		return nil, ErrIndexLimit
	case errors.Is(err, sqlread.ErrBusy):
		return nil, ErrBusy
	case errors.Is(err, ErrIndexLimit), errors.Is(err, ErrIndexInvalid), errors.Is(err, ErrScopeNotFound), errors.Is(err, sqlread.ErrSnapshotRequesterLimit), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return nil, err
	default:
		return nil, ErrIndexInvalid
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	return rows, nil
}
