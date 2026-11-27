package testenv

import (
	"encoding/json"

	"hedvi.dev/api/internal/sesevent"
)

func jsonUnmarshal(b []byte, v any) error         { return json.Unmarshal(b, v) }
func jsonMarshal(v any) ([]byte, error)           { return json.Marshal(v) }
func parseEvent(b []byte) (sesevent.Event, error) { return sesevent.Parse(b) }
