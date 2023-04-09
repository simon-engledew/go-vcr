// Package vcr contains a function for recording VCR cassettes.
package vcr

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/require"
)

type Response struct {
	Status struct {
		Code    int     `yaml:"code"`
		Message *string `yaml:"message"`
	} `yaml:"status"`
	Headers http.Header `yaml:"headers"`
	Body    struct {
		Encoding string `yaml:"encoding"`
		String   string `yaml:"string"`
	} `yaml:"body"`
	HttpVersion any `yaml:"http_version"`
}

type cassette struct {
	Interactions []*struct {
		Request *struct {
			Method string `yaml:"method"`
			URI    string `yaml:"uri"`
			Body   *struct {
				Encoding string `yaml:"encoding"`
				String   string `yaml:"string"`
			} `yaml:"body"`
			Headers http.Header `yaml:"headers"`
			Form    url.Values  `yaml:"form,omitempty"`
		} `yaml:"request"`
		Response   *Response `yaml:"response"`
		RecordedAt string    `yaml:"recorded_at"`
	} `yaml:"http_interactions"`
	RecordedWith string `yaml:"recorded_with"`
}

func Open(r io.Reader) (*cassette, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	var tape cassette
	if err := decoder.Decode(&tape); err != nil {
		return nil, err
	}
	return &tape, nil
}

func (c *cassette) Encode(w io.Writer) error {
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	return encoder.Encode(&c)
}

func normalizeJson(input string) string {
	var decoded interface{}
	if err := json.Unmarshal([]byte(input), &decoded); err == nil {
		if encoded, err := json.MarshalIndent(&decoded, "", "  "); err == nil {
			return string(encoded)
		}
	}
	return input
}

// replay a VCR and check for updates
func replay(t *testing.T, handler http.Handler, tape *cassette) {
	t.Helper()
	for _, interaction := range tape.Interactions {
		requestURI, err := url.Parse(interaction.Request.URI)
		require.NoError(t, err)

		recorder := httptest.NewRecorder()

		request := &http.Request{
			Method: strings.ToUpper(interaction.Request.Method),
			URL:    requestURI,
			Body:   io.NopCloser(strings.NewReader(interaction.Request.Body.String)),
			Header: interaction.Request.Headers,
		}

		handler.ServeHTTP(recorder, request)

		response := recorder.Result()

		if interaction.Response != nil && interaction.Response.Status.Code != response.StatusCode {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			require.Equalf(t, interaction.Response.Status.Code, response.StatusCode, "response for %v does not match recording: %s", requestURI.Path, string(body))
		}

		// we do not need the response body, however it must be closed to avoid resource leaks
		_ = response.Body.Close()

		body := recorder.Body.String()

		var contentType string
		if response.Header != nil {
			contentType = response.Header.Get("Content-Type")
		}
		if contentType == "application/json" {
			// protobuf randomly inserts spaces and so you cannot reliably compare json strings
			// re-encode using the standard library
			body = normalizeJson(body)
		}

		recording := &Response{}
		recording.Status.Code = recorder.Code
		recording.Body.Encoding = "UTF-8"
		recording.Body.String = body
		recording.Headers = response.Header
		recording.Headers.Set("Content-Length", strconv.Itoa(len(body)))

		if interaction.RecordedAt != "" {
			// check that the recorded at is valid
			_, err = time.Parse(http.TimeFormat, interaction.RecordedAt)
			require.NoError(t, err)
		}

		// reduce the noise in diffs by only updating the timestamp of things
		// that have changed
		if isResponseModified(interaction.Response, recording) {
			interaction.Response = recording
			interaction.RecordedAt = time.Now().UTC().Format(http.TimeFormat)
		}
	}
}

// timestampPattern will match a RFC3339 timestamp
var timestampPattern = regexp.MustCompile(`([0-9]+)-(0[1-9]|1[012])-(0[1-9]|[12][0-9]|3[01])[Tt]([01][0-9]|2[0-3]):([0-5][0-9]):([0-5][0-9]|60)([.][0-9]+)?(([Zz])|([+|-]([01][0-9]|2[0-3]):[0-5][0-9]))`)

// uuidPattern will match a hex representation of a v4 uuid/guid
var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}`)

// normalizeResponse strips out anything from response that may change between runs
// but has no effect on the equality of a response
func normalizeResponse(response *Response) *Response {
	if response == nil {
		return nil
	}

	clone := &Response{}
	*clone = *response

	body := response.Body.String

	// rewrite all the timestamps to be the same
	body = timestampPattern.ReplaceAllLiteralString(body, "0001-01-01T00:00:00Z")
	// Rewrite v4 UUIDs to a known value. To keep the value of the GUID intact use a non v4 UUID.
	body = uuidPattern.ReplaceAllLiteralString(body, "11111111-2222-3333-4444-000000000000")

	response.Body.String = body

	if response.Headers != nil {
		clone.Headers = response.Headers.Clone()
		clone.Headers.Set("Content-Length", "0")
	}

	return clone
}

func isResponseModified(before *Response, after *Response) bool {
	return !reflect.DeepEqual(normalizeResponse(before), normalizeResponse(after))
}

// overwriteTape loads the cassette at name and then replaces it after any modifications have been performed by fn
func overwriteTape(t *testing.T, path string, handler http.Handler) {
	t.Helper()

	fd, err := os.Open(path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, fd.Close())
	}()

	// create a separate file and atomically move it into place
	// leave the file if anything goes wrong so that the user can inspect the result
	tmp, err := os.Create(fd.Name() + ".tmp")
	require.NoError(t, err)
	defer tmp.Close()

	// signpost how this cassette was updated with a callback
	_, file, _, _ := runtime.Caller(2)

	pkg := reflect.Indirect(reflect.ValueOf(handler)).Type().PkgPath()

	_, err = fmt.Fprintf(tmp, "# generated by %s/%s\n---\n", pkg, filepath.Base(file))
	require.NoError(t, err)

	tape, err := Open(fd)
	require.NoError(t, err)

	replay(t, handler, tape)

	err = tape.Encode(tmp)
	require.NoError(t, err)

	err = tmp.Close()
	require.NoError(t, err)

	require.NoError(t, os.Rename(tmp.Name(), fd.Name()))
}

// diffTape loads the tape and returns an error if it was modified by fn
func diffTape(t *testing.T, path string, handler http.Handler) {
	t.Helper()
	fd, err := os.Open(path)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, fd.Close())
	}()

	var before bytes.Buffer
	var after bytes.Buffer

	tape, err := Open(fd)
	require.NoError(t, err)

	// re-encode to ignore comments or any formatting differences
	err = tape.Encode(&before)
	require.NoError(t, err)

	replay(t, handler, tape)

	err = tape.Encode(&after)
	require.NoError(t, err)

	require.Equal(t, before.String(), after.String(), "cassette has changed. run this test with the -overwrite flag and commit the result if this change looks legitimate", os.Args[0])
}

var overwrite = flag.Bool("overwrite", false, "Overwrite existing cassettes")

func Replay(t *testing.T, name string, handler http.Handler) {
	t.Helper()

	fn := diffTape

	if *overwrite {
		fn = overwriteTape
	}

	fn(t, name, handler)
}
