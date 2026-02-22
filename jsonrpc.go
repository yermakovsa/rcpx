package rcpx

import (
	"bytes"
	"encoding/json"
)

var nonIdempotentMethods = map[string]struct{}{
	"eth_sendTransaction":    {},
	"eth_sendRawTransaction": {},
}

// parseJSONRPCMethod best-effort extracts method info from a JSON-RPC request
// body (single object or batch array).
//
// Safety rail: if any part of a batch is unknown/unparseable for method
// extraction, the batch is treated as non-idempotent. If ok is false, callers
// must treat the request as non-idempotent.
func parseJSONRPCMethod(body []byte) (method string, batch bool, nonIdempotent bool, ok bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", false, false, false
	}

	switch trimmed[0] {
	case '{':
		m, ok := extractMethodFromObject(trimmed)
		if !ok {
			return "", false, false, false
		}
		_, ni := nonIdempotentMethods[m]
		return m, false, ni, true

	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return "", true, false, false
		}

		firstMethod := ""
		anyNI := false
		unknown := false

		for _, item := range items {
			itemBytes := bytes.TrimSpace(item)
			if len(itemBytes) == 0 || itemBytes[0] != '{' {
				unknown = true
				continue
			}

			m, ok := extractMethodFromObject(itemBytes)
			if !ok {
				unknown = true
				continue
			}
			if firstMethod == "" {
				firstMethod = m
			}
			if _, ni := nonIdempotentMethods[m]; ni {
				anyNI = true
			}
		}

		if firstMethod == "" {
			// No method extracted anywhere; callers must treat ok=false as non-idempotent.
			return "", true, unknown || anyNI, false
		}
		return firstMethod, true, unknown || anyNI, true

	default:
		return "", false, false, false
	}
}

func extractMethodFromObject(obj []byte) (string, bool) {
	var req struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(obj, &req); err != nil {
		return "", false
	}
	if req.Method == "" {
		return "", false
	}
	return req.Method, true
}
