package trigger

import (
	"context"
	"fmt"
	"regexp"

	"github.com/OwlShack/OwlShack/internal/config"
)

// Event is emitted by a trigger and passed to the template engine.
type Event struct {
	Cfg     config.TriggerConfig
	Type    string // group, dm, cron, cap, etc
	BotName string
	Data    map[string]any // Trigger-specific data available to templates
}

// Callback is called when a trigger fires.
type Callback func(Event)

// Trigger is the interface all trigger types implement.
type Trigger interface {
	// Start begins listening/polling; ctx controls the trigger's lifetime.
	Start(ctx context.Context, callback Callback) error

	// Stop shuts down the trigger and releases resources.
	Stop() error
}

// compilePatterns compiles a trigger's match list. A nil list means "match everything".
func compilePatterns(match *[]string) ([]*regexp.Regexp, error) {
	if match == nil {
		return nil, nil
	}
	patterns := make([]*regexp.Regexp, 0, len(*match))
	for _, m := range *match {
		re, err := regexp.Compile(m)
		if err != nil {
			return nil, fmt.Errorf("invalid match pattern %q: %w", m, err)
		}
		patterns = append(patterns, re)
	}
	return patterns, nil
}

// matchCaptures returns the first matching pattern's named captures, nil on no match, or an empty non-nil map when there are no patterns.
func matchCaptures(patterns []*regexp.Regexp, text string) map[string]string {
	if len(patterns) == 0 {
		return map[string]string{}
	}
	for _, re := range patterns {
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		captures := make(map[string]string)
		for i, name := range re.SubexpNames() {
			if i == 0 || name == "" {
				continue
			}
			captures[name] = m[i]
		}
		return captures
	}
	return nil
}

func patternStrings(patterns []*regexp.Regexp) []string {
	strs := make([]string, len(patterns))
	for i, re := range patterns {
		strs[i] = re.String()
	}
	return strs
}
