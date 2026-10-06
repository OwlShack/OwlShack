package trigger

import (
	"context"
	"log/slog"
	"strings"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/mmcdole/gofeed"
)

// RSSTrigger fires once per new item in an RSS, Atom or JSON feed, from the feed entry alone.
type RSSTrigger struct{ *feedPoller }

func NewRSSTrigger(botName string, cfg config.TriggerConfig, log *slog.Logger) (*RSSTrigger, error) {
	p, err := newFeedPoller("rss", botName, cfg, log)
	if err != nil {
		return nil, err
	}
	p.decode = decodeEntry
	return &RSSTrigger{p}, nil
}

// decodeEntry is what every feed carries, with no second fetch.
func decodeEntry(_ context.Context, feed *gofeed.Feed, item *gofeed.Item) (map[string]any, map[string]string, error) {
	data := map[string]any{
		"Feed":        feed.Title,
		"FeedLink":    feed.Link,
		"ID":          itemID(item),
		"Title":       item.Title,
		"Link":        item.Link,
		"Description": item.Description,
		"Content":     item.Content,
		"Author":      authorName(item),
		"Categories":  item.Categories,
		"Published":   itemTime(item),
		// The parsed item itself, for anything the convenience keys above do not flatten —
		// .Item.Enclosures, .Item.Extensions (where an Atom feed's cap: fields land), .Item.Custom.
		"Item": item,
	}
	fields := map[string]string{
		"title":       item.Title,
		"description": item.Description,
		"content":     item.Content,
		"link":        item.Link,
		"author":      authorName(item),
		"category":    strings.Join(item.Categories, "\n"),
	}
	return data, fields, nil
}

var _ Trigger = (*RSSTrigger)(nil)
