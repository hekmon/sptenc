package main

import (
	"encoding/base64"
	"fmt"
)

func main() {
	data := "bGlieDI2NTk4LTEtMS0xMTAwLTEtMS0x"
	decoded, err := base64.RawStdEncoding.DecodeString(data)
	if err != nil {
		panic(err)
	}
	fmt.Println(base64.RawURLEncoding.EncodeToString(decoded))
}
