package serialization

import (
	"bytes"
	"encoding/json"
)

func Serialize[T any](obj T) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	err := encoder.Encode(obj)
	return b.Bytes(), err
}

func Deserialize[T any](b []byte) (T, error) {
	var obj T
	buf := bytes.NewBuffer(b)
	decoder := json.NewDecoder(buf)
	err := decoder.Decode(&obj)
	return obj, err
}
