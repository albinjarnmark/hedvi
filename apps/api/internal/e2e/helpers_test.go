package e2e

import (
	"net/http"
	"time"

	"hedvi.dev/api/internal/netsafe"
)

func netsafeStrict() *http.Client {
	return netsafe.NewClient(netsafe.Options{Timeout: 2 * time.Second})
}
