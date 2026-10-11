package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// statFields returns the fields of /proc/<pid>/stat after the command name;
// fields[0] is field 3 (state).
func statFields(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, err
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return nil, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return nil, fmt.Errorf("short /proc/%d/stat", pid)
	}
	return f, nil
}

func procStart(pid int) (string, error) {
	f, err := statFields(pid)
	if err != nil {
		return "", err
	}
	return f[19], nil
}

// Alive reports whether pid is running and started at start, so a recycled
// pid is not taken for the original process.
func Alive(pid int, start string) bool {
	if pid <= 0 || start == "" {
		return false
	}
	s, err := procStart(pid)
	return err == nil && s == start
}

// ProcStart is the start time of pid, in clock ticks since boot.
func ProcStart(pid int) (string, error) { return procStart(pid) }

// KillStaleGroup kills what is left of a monitor's process group after its
// owner died. Only processes in that group that started no earlier than the
// group's leader are killed, so an unrelated group that reused the number
// is left alone. Returns how many processes were signalled.
func KillStaleGroup(pgid int, groupStart string) int {
	if pgid <= 1 || groupStart == "" {
		return 0
	}
	min, err := strconv.ParseUint(groupStart, 10, 64)
	if err != nil {
		return 0
	}
	ents, _ := filepath.Glob("/proc/[0-9]*")
	n := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(filepath.Base(e))
		if err != nil || pid == os.Getpid() {
			continue
		}
		f, err := statFields(pid)
		if err != nil {
			continue
		}
		// fields[2] is field 5 (pgrp).
		if pg, _ := strconv.Atoi(f[2]); pg != pgid {
			continue
		}
		if st, err := strconv.ParseUint(f[19], 10, 64); err != nil || st < min {
			continue
		}
		if syscall.Kill(pid, syscall.SIGKILL) == nil {
			n++
		}
	}
	return n
}
