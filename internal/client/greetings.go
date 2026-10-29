package client

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The hello-world data model: the project's greetings. Writes go through the
// records API; reads use the authenticated SQL reader and full-text search.

// MaxMessage is the longest greeting message, in characters.
const MaxMessage = 500

// greetingColumns are the SQL-readable greeting fields, in output order.
const greetingColumns = "id, message, created, updated"

// AddGreeting creates one greeting.
func (c *Client) AddGreeting(message string) (map[string]any, error) {
	message = strings.TrimSpace(message)
	if message == "" || !utf8.ValidString(message) || utf8.RuneCountInString(message) > MaxMessage {
		return nil, fail(2, "the message must be 1-%d characters of UTF-8 text", MaxMessage)
	}
	data, err := c.Must("POST", records("greetings"), map[string]string{"message": message})
	if err != nil {
		return nil, err
	}
	record, ok := data.(map[string]any)
	if !ok {
		return nil, fail(1, "unexpected response to the new greeting")
	}
	out := map[string]any{}
	for _, key := range strings.Split(greetingColumns, ", ") {
		out[key] = record[key]
	}
	return out, nil
}

// ListGreetings returns the newest greetings.
func (c *Client) ListGreetings(limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 500 {
		return nil, fail(2, "--limit must be 1-500")
	}
	return c.Rows(fmt.Sprintf("SELECT %s FROM greetings ORDER BY created DESC, id DESC LIMIT %d", greetingColumns, limit))
}

// Search ranks the greetings by full-text match of their message.
func (c *Client) Search(term string, o SearchOptions) (map[string]any, error) {
	if strings.TrimSpace(term) == "" || len(term) > 4096 || len(strings.Fields(term)) > 16 {
		return nil, fail(2, "search requires 1-16 terms and at most 4096 UTF-8 bytes")
	}
	if o.Limit < 1 || o.Limit > 100 || o.Offset < 0 || o.Offset > 10000 {
		return nil, fail(2, "search limit must be 1-100 and offset 0-10000")
	}
	if o.Offset > 0 && o.Generation == "" {
		return nil, fail(2, "continuation requires --generation from the first page")
	}
	body := map[string]any{"index": "greetings", "query": term, "limit": o.Limit, "offset": o.Offset}
	if o.Generation != "" {
		body["expectedGeneration"] = o.Generation
	}
	data, err := c.Must("POST", "/api/context/search", body)
	if err != nil {
		if f, ok := err.(*Fail); ok && f.Code == 4 {
			return nil, fail(4, "the greetings changed; discard prior result pages and restart at offset 0 without --generation")
		}
		return nil, err
	}
	result, _ := data.(map[string]any)
	hits, _ := result["hits"].([]any)
	generation, _ := result["generation"].(string)
	hasMore, _ := result["hasMore"].(bool)
	if result["index"] != "greetings" || generation == "" || (o.Generation != "" && generation != o.Generation) || len(hits) > o.Limit {
		return nil, fail(1, "invalid search response")
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		id, _ := dig(h, "id").(string)
		if !idPattern.MatchString(id) {
			return nil, fail(1, "invalid search hit")
		}
		ids = append(ids, literal(id))
	}
	messages := map[string]map[string]any{}
	if len(ids) > 0 {
		rows, err := c.Rows(fmt.Sprintf("SELECT %s FROM greetings WHERE id IN (%s)", greetingColumns, strings.Join(ids, ",")))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			id, _ := row["id"].(string)
			messages[id] = row
		}
	}
	greetings := []map[string]any{}
	for _, h := range hits {
		id, _ := dig(h, "id").(string)
		row, ok := messages[id]
		if !ok {
			continue // deleted after the search read
		}
		row["score"], row["excerpt"] = dig(h, "score"), dig(h, "excerpt")
		greetings = append(greetings, row)
	}
	var next any
	if hasMore {
		next = o.Offset + len(hits)
	}
	return map[string]any{"query": term, "generation": generation, "greetings": greetings, "hasMore": hasMore, "nextOffset": next}, nil
}
