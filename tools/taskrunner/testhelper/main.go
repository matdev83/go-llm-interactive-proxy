package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func main() {
	mode := flag.String("mode", "success", "helper mode")
	exit := flag.Int("exit", 1, "exit status")
	key := flag.String("key", "", "environment key")
	size := flag.Int("size", 0, "output size")
	text := flag.String("text", "", "text to print")
	marker := flag.String("marker", "", "failure marker")
	readyFile := flag.String("ready-file", "", "grandchild ready marker")
	pidFile := flag.String("pid-file", "", "grandchild pid marker")
	flag.Parse()

	switch *mode {
	case "success":
		fmt.Println("success")
	case "fail":
		os.Exit(*exit)
	case "sleep":
		for {
			time.Sleep(time.Second)
		}
	case "print-cwd":
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		fmt.Println(cwd)
	case "print-env":
		fmt.Println(os.Getenv(*key))
	case "output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("O", *size/2)+strings.Repeat("o", *size-*size/2))
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("E", *size/2)+strings.Repeat("e", *size-*size/2))
	case "print":
		fmt.Print(*text)
	case "fail-output":
		fmt.Fprint(os.Stdout, *marker)
		fmt.Fprint(os.Stderr, "child stderr")
		os.Exit(*exit)
	case "grandchild":
		if *readyFile != "" {
			if err := os.WriteFile(*readyFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				panic(err)
			}
		}
		for {
			time.Sleep(time.Second)
		}
	case "spawn-grandchild":
		self, err := os.Executable()
		if err != nil {
			panic(err)
		}
		child := exec.Command(self, "-mode=grandchild", "-ready-file", *readyFile)
		if err := child.Start(); err != nil {
			panic(err)
		}
		if *pidFile != "" {
			if err := os.WriteFile(*pidFile, []byte(strconv.FormatInt(int64(child.Process.Pid), 10)), 0o600); err != nil {
				panic(err)
			}
		}
		for {
			time.Sleep(time.Second)
		}
	default:
		panic("unknown mode")
	}
}
