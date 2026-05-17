package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestMain(t *testing.T) {
	if os.Getenv("BE_MAIN") == "1" {
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "--help")
	cmd.Env = append(os.Environ(), "BE_MAIN=1")
	err := cmd.Run()
	if err != nil {
		t.Fatalf("process ran with err %v, want exit status 0", err)
	}
}
