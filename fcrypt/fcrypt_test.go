package fcrypt

import (
	"bytes"
	"testing"
)

type pkcs7Case struct {
	in, out   []byte
	blocksize int
	ok        bool
}

var pkcs7Tests = []pkcs7Case{
	{[]byte{1, 2, 3, 4, 5, 6, 7, 1}, []byte{1, 2, 3, 4, 5, 6, 7}, 8, true},
	{[]byte{1, 2, 3, 4, 5, 6, 7, 8}, []byte{}, 8, false},
	{[]byte{1, 2, 3, 4, 5, 6, 7, 8,
		8, 8, 8, 8, 8, 8, 8, 8}, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 8, true},
	{[]byte{1, 2, 3, 4, 4, 4, 4, 4}, []byte{1, 2, 3, 4}, 8, true},
	{[]byte{1, 2, 3, 4, 4, 4, 4}, []byte{}, 8, false},
}

func TestRemovePkcs7Padding(t *testing.T) {
	for _, c := range pkcs7Tests {
		out, err := removePkcs7Padding(c.in, c.blocksize)
		t.Log("Err", err)
		if (err == nil) != c.ok {
			t.Errorf("Got unexpected error %v value on test %#v", err, c)
		}
		if !bytes.Equal(c.out, out) {
			t.Errorf("Got unexpected value %#v in test %#v", out, c)
		}
	}
}
