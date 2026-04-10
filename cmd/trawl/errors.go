package main

import "fmt"

func errNotImplemented(cmd string) error {
	return fmt.Errorf("%s: not implemented yet", cmd)
}
