package canonicaljson

import (
	"testing"
)

func TestMarshalSortsMembersAndRejectsDuplicates(t *testing.T) {
	canonical, err := Marshal([]byte(`{"z":1,"a":[true,{"b":2,"a":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"a":[true,{"a":3,"b":2}],"z":1}` {
		t.Fatalf("canonical bytes=%s", canonical)
	}
	if _, err := Marshal([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate member was accepted")
	}
}
