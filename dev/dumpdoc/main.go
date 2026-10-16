package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

func main() {
	b, _ := hex.DecodeString(strings.TrimSpace(os.Args[1]))
	m := &commonpb.MapValue{}
	if err := proto.Unmarshal(b, m); err != nil {
		panic(err)
	}
	fmt.Println(prototext.Format(m))
}
