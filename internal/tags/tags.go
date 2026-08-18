// Package tags provides functionality for parsing configuration intent tags
// and registering tag flags with command-line flag sets.
package tags

import (
	"flag"
	"slices"
	"strings"
)

// Flags manages the command-line flags for specifying service tags.
type Flags struct {
	tags string
	tag  string
}

// RegisterFlags registers the -tags and -tag flags on the default flag set (flag.CommandLine).
func RegisterFlags() *Flags {
	return RegisterFlagSet(flag.CommandLine)
}

// RegisterFlagSet registers the -tags and -tag flags on the provided FlagSet.
func RegisterFlagSet(fs *flag.FlagSet) *Flags {
	f := &Flags{}
	f.Register(fs)
	return f
}

// Register registers the -tags and -tag flags on the provided FlagSet.
func (f *Flags) Register(fs *flag.FlagSet) {
	if fs == nil {
		fs = flag.CommandLine
	}
	fs.StringVar(&f.tags, "tags", "", "Comma-separated list of tags describing configuration intent (e.g. cache, source)")
	fs.StringVar(&f.tag, "tag", "", "Tag describing configuration intent (e.g. cache, source)")
}

// Tags parses and returns a deduplicated slice of trimmed tag strings from the registered flags.
func (f *Flags) Tags() []string {
	if f == nil {
		return nil
	}
	return Parse(f.tags, f.tag)
}

// Parse parses one or more comma-separated strings into a deduplicated slice of non-empty, trimmed tag strings.
func Parse(inputs ...string) []string {
	var result []string
	for _, input := range inputs {
		for t := range strings.SplitSeq(input, ",") {
			t = strings.TrimSpace(t)
			if t != "" && !slices.Contains(result, t) {
				result = append(result, t)
			}
		}
	}
	return result
}
