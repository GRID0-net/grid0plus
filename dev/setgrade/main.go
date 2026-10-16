package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"google.golang.org/protobuf/proto"
)

// setgrade <hex> <total_grade_point>: prints the re-encoded hex.
func main() {
	b, err := hex.DecodeString(strings.TrimSpace(os.Args[1]))
	if err != nil {
		panic(err)
	}
	p, _ := strconv.ParseInt(os.Args[2], 10, 64)
	m := &commonpb.MapValue{}
	if err := proto.Unmarshal(b, m); err != nil {
		panic(err)
	}
	if m.Fields == nil {
		m.Fields = map[string]*commonpb.Value{}
	}
	fmt.Fprintln(os.Stderr, "before:", m.Fields["total_grade_point"], m.Fields["grade"])
	m.Fields["total_grade_point"] = &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: p}}
	m.Fields["grade"] = &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: min(p/100, 8)}}
	out, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	fmt.Println(hex.EncodeToString(out))
}
