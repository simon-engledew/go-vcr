package vcr

import (
	"net/http"
	"regexp"
)

func ReplacePattern(pattern *regexp.Regexp, repl string) NormalizeOption {
	return func(resp *Response) {
		resp.Body.String = pattern.ReplaceAllLiteralString(resp.Body.String, repl)
	}
}

var ReplaceTimestamps = func() NormalizeOption {
	// timestampPattern will match a RFC3339 timestamp
	timestampPattern := regexp.MustCompile(`([0-9]+)-(0[1-9]|1[012])-(0[1-9]|[12][0-9]|3[01])[Tt]([01][0-9]|2[0-3]):([0-5][0-9]):([0-5][0-9]|60)([.][0-9]+)?(([Zz])|([+|-]([01][0-9]|2[0-3]):[0-5][0-9]))`)
	return ReplacePattern(timestampPattern, "0001-01-01T00:00:00Z")
}()

var ReplaceUUIDs = func() NormalizeOption {
	// uuidPattern will match a hex representation of a v4 uuid/guid
	var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}`)
	return ReplacePattern(uuidPattern, "11111111-2222-3333-4444-000000000000")
}()

// normalize clones response and applies opts to strip out anything that changes between runs but does
// not affect the equality of the responses.
func normalize(response *Response, opts []NormalizeOption) *Response {
	if response == nil {
		return nil
	}

	clone := &Response{}
	*clone = *response

	body := response.Body.String

	response.Body.String = body

	clone.Headers = response.Headers.Clone()

	if clone.Headers == nil {
		clone.Headers = http.Header{}
	}

	for _, opt := range opts {
		opt(clone)
	}

	return clone
}
