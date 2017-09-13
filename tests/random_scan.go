package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

func request(c chan int) {
	x := rand.Intn(1000)
	resp, err := http.Get("http://127.0.0.1:8036/riak/test/key" + strconv.Itoa(x))
	if err != nil {
		fmt.Println("FAIL", err)
	}
	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		fmt.Println("FAIL", resp.StatusCode)
	} else {
		fmt.Print(".")
	}
	_ = <-c
}

func main() {
	var i int
	c := make(chan int, 50)
	rand.Seed(42)

	for ; i < 1000; i++ {
		c <- 1
		go request(c)
	}

	for len(c) > 0 {
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Println("DONE", len(c))
}
