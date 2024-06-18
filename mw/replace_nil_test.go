package mw

import (
	"encoding/json"
	"fmt"
	"testing"
)

type A struct {
	B  *B
	BB B
	C  []int
	D  map[string]string
	E  interface{}
	F  *int
}

type B struct {
	D *C
	E []int
}

type C struct {
}

func TestReplaceNil(t *testing.T) {
	a := &A{}
	k := any(a)
	ReplaceNil(&k)
	printJson(k)
	// {"B":null,"BB":{"D":null,"E":[]},"C":[],"D":{},"E":null,"F":null}

	var b []*A
	k = any(b)
	ReplaceNil(&k)
	printJson(k)
	// {}

	i := 5
	c := &A{
		B: nil,
		BB: B{
			D: &C{},
			E: []int{1, 2, 5, 3, 6},
		},
		C: []int{1, 1, 1},
		D: map[string]string{
			"a": "A",
			"b": "B",
		},
		E: map[int]int{
			1: 11,
			2: 22,
		},
		F: &i,
	}
	k = any(c)
	ReplaceNil(&k)
	printJson(k)
	// {"B":null,"BB":{"D":{},"E":[1,2,5,3,6]},"C":[1,1,1],"D":{"a":"A","b":"B"},"E":{"1":11,"2":22},"F":5}

}

func printJson(data any) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		fmt.Println("Error serializing to JSON:", err)
		return
	}
	fmt.Println(string(jsonData)) // 输出: {}
}
