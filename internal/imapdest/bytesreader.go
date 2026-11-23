package imapdest

import (
	"bufio"
	"bytes"
	"strings"
)

func bytesReader(b []byte) *bufio.Reader { return bufio.NewReader(bytes.NewReader(b)) }

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
