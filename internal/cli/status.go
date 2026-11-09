package cli

import (
	"errors"
	"strings"

	"connectrpc.com/connect"
)

// StatusURL is hosted Stagd's status page.
const StatusURL = "https://status.stagd.dev"

// StatusHint is a line to print after err when the trouble may be hosted
// Stagd's own rather than the command's: the API failing or not answering.
// Empty for anything else, and for another API than hosted Stagd's.
func StatusHint(err error, apiURL string) string {
	if strings.TrimRight(apiURL, "/") != DefaultAPIURL {
		return ""
	}

	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeInternal, connect.CodeUnknown:
	default:
		return ""
	}

	// CodeOf says Unknown for any error that isn't Connect's, such as a flag
	// that doesn't parse: only the API's own count.
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return ""
	}

	return "Stagd may be having trouble: see " + StatusURL
}
