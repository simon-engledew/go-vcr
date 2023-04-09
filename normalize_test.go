package vcr_test

import (
	"fmt"
	"github.com/simon-engledew/go-vcr"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplaceTimestamps(t *testing.T) {
	resp := &vcr.Response{}
	resp.Body.String = "RFC3339 timestamp 1990-12-31T23:59:60Z"
	vcr.ReplaceTimestamps(resp)
	require.NotEqual(t, resp.Body.String, "RFC3339 timestamp 1990-12-31T23:59:60Z")
}

func TestReplaceUUIDs(t *testing.T) {
	resp := &vcr.Response{}
	resp.Body.String = "UUID 123e4567-e89b-42d3-a456-426614174000"
	vcr.ReplaceUUIDs(resp)
	fmt.Println(resp.Body.String)
	require.NotEqual(t, resp.Body.String, "UUID 123e4567-e89b-42d3-a456-426614174000")
}
