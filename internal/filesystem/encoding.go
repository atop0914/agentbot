package filesystem

import (
	"encoding/base64"
	"fmt"
)

// decodeBase64 解码 base64 内容
func decodeBase64(data []byte) ([]byte, error) {
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Decode(decoded, data)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return decoded[:n], nil
}
