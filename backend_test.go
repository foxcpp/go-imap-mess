package mess_test

import (
	"math/rand"
	"testing"

	backendtests "github.com/foxcpp/go-imap-backend-tests"
	"github.com/foxcpp/go-imap-mess/memory"
)

func initBackend() backendtests.Backend {
	return memory.New()
}

func TestBackend(t *testing.T) {
	rand.Seed(1)
	backendtests.RunTests(t, initBackend, func (_ backendtests.Backend) {})
}