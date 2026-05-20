package launcherutils

import (
	"flag"
	"strings"
)

// normalizePathPrefix ensures a leading slash and no trailing slash, matching ADK
// sublauncher conventions (pubsub, api, etc.).
func NormalizePathPrefix(prefix string) string {
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimSuffix(prefix, "/")
}

// formatFlagUsage renders default flag help for [CommandLineSyntax].
func FormatFlagUsage(fs *flag.FlagSet) string {
	var b strings.Builder
	o := fs.Output()
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(o)
	return b.String()
}
