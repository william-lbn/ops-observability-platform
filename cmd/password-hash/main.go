package main

import (
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"os"
	"strings"
)

func main() {
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 1025))
	if e != nil || len(b) > 1024 {
		os.Exit(1)
	}
	password := strings.TrimSuffix(string(b), "\n")
	if len(password) < 16 || len(password) > 72 {
		os.Exit(1)
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		os.Exit(1)
	}
	fmt.Println(string(hash))
}
