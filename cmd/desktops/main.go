package main

import (
	"os"

	"github.com/viam-labs/screenshot-cam/subproc"
)

func main() {
	if os.Args[1] == "parent" {
		println("PARENT MODE")
		if err := subproc.SpawnSelf(" child"); err != nil {
			panic(err)
		}
	} else if os.Args[1] == "child" {
		println("CHILD MODE")
		f, err := os.Create("child.txt")
		if err != nil {
			panic(err)
		}
		defer f.Close()
		f.Write([]byte("HELLO"))
	} else {
		panic("please pass 'parent' or 'child' as first argument")
	}
}
