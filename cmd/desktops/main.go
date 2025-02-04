package main

import "github.com/viam-labs/screenshot-cam/subproc"

func main() {
	if err := subproc.SpawnSelf(" dump"); err != nil {
		panic(err)
	}
}
