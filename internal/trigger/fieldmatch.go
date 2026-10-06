package trigger

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/OwlShack/OwlShack/internal/config"
)

// fieldMatcher holds a feed trigger's patterns grouped by the field each names. Within a field the
// patterns are alternatives; across fields they are all required. That is the useful pair: regex
// alternation already says "Extreme or Severe" inside one field, and nothing a single regex can do
// says "this severity AND that area".
type fieldMatcher map[string][]*regexp.Regexp

func newFieldMatcher(match *[]string) (fieldMatcher, error) {
	if match == nil || len(*match) == 0 {
		return nil, nil
	}
	m := fieldMatcher{}
	for _, entry := range *match {
		field, pattern, ok := config.SplitFieldPattern(entry)
		if !ok || field == "" {
			return nil, fmt.Errorf("match %q must name a field, as \"<field>:<pattern>\"", entry)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid match pattern %q: %w", pattern, err)
		}
		m[field] = append(m[field], re)
	}
	return m, nil
}

// match reports whether an item satisfies every field, returning the named captures of the patterns
// that hit. A nil matcher matches everything, which is how a feed trigger with no patterns sends
// every item.
func (m fieldMatcher) match(fields map[string]string) map[string]string {
	captures := map[string]string{}
	for _, field := range slices.Sorted(maps.Keys(m)) {
		text, known := fields[field]
		if !known {
			return nil // the decoder offers no such field, so nothing can satisfy it
		}
		hit := false
		for _, re := range m[field] {
			sub := re.FindStringSubmatch(text)
			if sub == nil {
				continue
			}
			hit = true
			for i, name := range re.SubexpNames() {
				if i > 0 && name != "" {
					captures[name] = sub[i]
				}
			}
			break // alternatives within a field: the first hit settles it
		}
		if !hit {
			return nil
		}
	}
	return captures
}
