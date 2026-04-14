package pdf

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
)

// runPdfinfo invokes pdfinfo on the given PDF bytes and returns an Info
// populated with whatever fields parsed successfully. Missing binary or
// invocation error returns a zero Info rather than an error — metadata
// is best-effort and must not fail the extraction pipeline.
func runPdfinfo(ctx context.Context, body []byte) *Info {
	if !HasPdfinfo() {
		return &Info{}
	}
	out, err := runCmd(ctx, 0, "pdfinfo", body, "-")
	if err != nil {
		return &Info{}
	}
	return parsePdfinfoOutput(out)
}

// parsePdfinfoOutput parses pdfinfo's stdout into an Info struct. Output
// is one key-value pair per line, separated by a colon and whitespace:
//
//	Title:           Sample Document
//	Author:          Jane Doe
//	CreationDate:    Fri Mar 15 10:00:00 2024 UTC
//	Pages:           12
//
// Unknown keys and malformed lines are ignored. Empty values are treated
// as absent.
func parsePdfinfoOutput(out []byte) *Info {
	info := &Info{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if val == "" {
			continue
		}
		switch key {
		case "Title":
			info.Title = val
		case "Author":
			info.Author = val
		case "Pages":
			if n, err := strconv.Atoi(val); err == nil {
				info.PageCount = n
			}
		case "CreationDate":
			if t, ok := parsePdfinfoDate(val); ok {
				info.CreatedAt = t
			}
		}
	}
	return info
}

// parsePdfinfoDate parses the human-readable date format pdfinfo emits.
// Poppler writes dates as "Fri Mar 15 10:00:00 2024 UTC" (with timezone
// in recent versions, omitted in older ones). `_2` in the layout accepts
// the space-padded single-digit day some versions produce.
func parsePdfinfoDate(s string) (time.Time, bool) {
	layouts := []string{
		"Mon Jan 2 15:04:05 2006 MST",
		"Mon Jan _2 15:04:05 2006 MST",
		"Mon Jan 2 15:04:05 2006",
		"Mon Jan _2 15:04:05 2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
