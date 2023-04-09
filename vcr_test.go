package vcr_test

import (
	"github.com/simon-engledew/go-vcr"
	"net/http"
	"testing"
)

func TestReplay(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello-world", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Hello world!", 200)
	})
	vcr.Replay(t, "vcr_test.yml", mux)
}
