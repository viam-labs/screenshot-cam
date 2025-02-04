package main

func main() {
	if err := spawnSelf(" dump"); err != nil {
		panic(err)
	}
}
