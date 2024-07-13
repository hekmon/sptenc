package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

type Children struct {
	children []*os.Process
	access   sync.Mutex
}

func (c *Children) Add(process *os.Process) {
	defer c.access.Unlock()
	c.access.Lock()
	c.children = append(c.children, process)
}

func (c *Children) Remove(process *os.Process) {
	defer c.access.Unlock()
	c.access.Lock()
	for i, existingChild := range c.children {
		if existingChild == process {
			c.children = append(c.children[:i], c.children[i+1:]...)
			break
		}
	}
}

func (c *Children) StopAndWait() (err error) {
	// Prepare
	defer c.access.Unlock()
	c.access.Lock()
	var doneChildren sync.WaitGroup
	doneChildren.Add(len(c.children))
	errors := make([]string, 0, len(c.children))
	var errorsAccess sync.Mutex
	// Stop all children concurrently, and wait for them to finish.
	for _, child := range c.children {
		go func(p *os.Process) {
			if err := StopAndWait(p); err != nil {
				errorsAccess.Lock()
				errors = append(errors, err.Error())
				errorsAccess.Unlock()
			}
			doneChildren.Done()
		}(child)
	}
	// Wait and return errors if any
	doneChildren.Wait()
	if len(errors) > 0 {
		err = fmt.Errorf("encoutered %d errors: %s", len(errors), strings.Join(errors, ", "))
	}
	return err
}
