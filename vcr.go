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
	"runtime"
	"slices"
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
		Request struct {
			Method string `yaml:"method"`
			URI    string `yaml:"uri"`
			Body   *struct {
				Encoding string `yaml:"encoding"`
				String   string `yaml:"string"`
			} `yaml:"body,omitempty"`
			Headers http.Header `yaml:"headers"`
			Form    url.Values  `yaml:"form,omitempty"`
		} `yaml:"request"`
		Response   *Response `yaml:"response"`
		RecordedAt string    `yaml:"recorded_at"`
	} `yaml:"http_interactions"`
	RecordedWith string `yaml:"recorded_with"`
}

func open(r io.Reader) (*cassette, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	var tape cassette
	if err := decoder.Decode(&tape); err != nil {
		return nil, err
	}
	return &tape, nil
}

func encode(w io.Writer, c *cassette) error {
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	return encoder.Encode(c)
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
func replay(t *testing.T, handler http.Handler, tape *cassette, opts []NormalizeOption) {
	t.Helper()
	for _, interaction := range tape.Interactions {
		requestURI, err := url.Parse(interaction.Request.URI)
		require.NoError(t, err)

		recorder := httptest.NewRecorder()

		var requestBody io.ReadCloser
		if interaction.Request.Body != nil {
			requestBody = io.NopCloser(strings.NewReader(interaction.Request.Body.String))
		}

		request := &http.Request{
			Method: strings.ToUpper(interaction.Request.Method),
			URL:    requestURI,
			Body:   requestBody,
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
		if isResponseModified(interaction.Response, recording, opts) {
			interaction.Response = recording
			interaction.RecordedAt = time.Now().UTC().Format(http.TimeFormat)
		}
	}
}

func isResponseModified(before *Response, after *Response, opts []NormalizeOption) bool {
	return !reflect.DeepEqual(normalize(before, opts), normalize(after, opts))
}

func findModuleRoot(dir string) (roots string) {
	if dir == "" {
		panic("dir not set")
	}
	dir = filepath.Clean(dir)

	// Look for enclosing go.mod.
	for {
		if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !fi.IsDir() {
			return dir
		}
		d := filepath.Dir(dir)
		if d == dir {
			break
		}
		dir = d
	}
	return ""
}

var MaxTestSearchDepth = 20

func findTest(t *testing.T) string {
	t.Helper()
	rpc := make([]uintptr, MaxTestSearchDepth)
	size := runtime.Callers(0, rpc)
	frames := rpc[:size]
	require.Greater(t, size, 0, "could not determine caller")
	slices.Reverse(frames)
	iter := runtime.CallersFrames(frames)
	for {
		frame, more := iter.Next()

		if strings.HasSuffix(filepath.Base(frame.File), "_test.go") {
			root := findModuleRoot(filepath.Dir(frame.File))

			path, err := filepath.Rel(root, frame.File)
			require.NoError(t, err)
			return path
		}

		require.True(t, more, "no test found within %d stack frames", MaxTestSearchDepth)
	}
}

// overwriteTape loads the cassette at name and then replaces it after any modifications have been performed by fn
func overwriteTape(t *testing.T, path string, handler http.Handler, opts []NormalizeOption) {
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
	test := findTest(t)

	_, err = fmt.Fprintf(tmp, "# generated by %s\n---\n", test)
	require.NoError(t, err)

	tape, err := open(fd)
	require.NoError(t, err)

	replay(t, handler, tape, opts)

	err = encode(tmp, tape)
	require.NoError(t, err)

	err = tmp.Close()
	require.NoError(t, err)

	require.NoError(t, os.Rename(tmp.Name(), fd.Name()))
}

// diffTape loads the tape and returns an error if it was modified by fn
func diffTape(t *testing.T, path string, handler http.Handler, opts []NormalizeOption) {
	t.Helper()
	fd, err := os.Open(path)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, fd.Close())
	}()

	var before bytes.Buffer
	var after bytes.Buffer

	tape, err := open(fd)
	require.NoError(t, err)

	// re-encode to ignore comments or any formatting differences
	err = encode(&before, tape)
	require.NoError(t, err)

	replay(t, handler, tape, opts)

	err = encode(&after, tape)
	require.NoError(t, err)

	require.Equal(t, before.String(), after.String(), "cassette has changed. run this test with the -overwrite flag and commit the result if this change looks legitimate", os.Args[0])
}

var overwrite = flag.Bool("overwrite", false, "Overwrite existing cassettes")

type NormalizeOption func(*Response)

func Replay(t *testing.T, name string, handler http.Handler, opts ...NormalizeOption) {
	t.Helper()

	fn := diffTape

	if *overwrite {
		fn = overwriteTape
	}

	fn(t, name, handler, opts)
}
