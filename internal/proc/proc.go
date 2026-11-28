// Package proc reads what gantline needs about other processes: the process
// tree, and a process's name, argv, environment and working directory. It
// wraps gopsutil, which reads /proc on Linux and sysctl and libproc on macOS.
package proc

import (
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// Tree maps each pid to its children, from one snapshot of the process
// table. gopsutil's own Children rescans the whole table on macOS, so a
// walk takes one snapshot and reads it.
type Tree map[int][]int

// Snapshot reads the process table once.
func Snapshot() (Tree, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	t := make(Tree, len(procs))
	for _, p := range procs {
		ppid, err := p.Ppid()
		if err != nil {
			continue // exited since the listing
		}
		t[int(ppid)] = append(t[int(ppid)], int(p.Pid))
	}
	return t, nil
}

// Pids lists every process.
func Pids() ([]int, error) {
	ps, err := process.Pids()
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = int(p)
	}
	return out, err
}

// Name is the process's short name, such as "claude", or "" when it can't be read.
func Name(pid int) string {
	p := &process.Process{Pid: int32(pid)}
	n, _ := p.Name()
	return n
}

// Argv is the process's command line, one entry per argument.
func Argv(pid int) ([]string, error) {
	p := &process.Process{Pid: int32(pid)}
	return p.CmdlineSlice()
}

// Env is the environment the process was started with, without empty entries.
//
// On macOS it can be inexact: gopsutil finds the end of the environment by
// the padding XNU leaves before its own "apple" strings, and when there is
// none those strings (ptr_munge=, stack_guard=, ...) read as variables.
// Env drops the ones XNU is known to write. An empty variable ends the
// environment early on macOS, losing any variables after it.
func Env(pid int) ([]string, error) {
	p := &process.Process{Pid: int32(pid)}
	env, err := p.Environ()
	if err != nil {
		return nil, err
	}
	out := env[:0]
	for _, e := range env {
		if e == "" {
			continue
		}
		if runtime.GOOS == "darwin" {
			if k, _, _ := strings.Cut(e, "="); appleStrings[k] {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// appleStrings are the keys of the strings XNU appends after a process's
// environment (bsd/kern/kern_exec.c, exec_add_apple_strings).
var appleStrings = map[string]bool{
	"executable_path": true, "pfz": true, "stack_guard": true, "malloc_entropy": true,
	"ptr_munge": true, "main_stack": true, "executable_file": true, "dyld_file": true,
	"executable_cdhash": true, "executable_boothash": true, "th_port": true,
	"arm64e_abi": true, "subsystem_root_path": true, "executable_mtime": true,
}

// Cwd is the process's working directory.
func Cwd(pid int) (string, error) {
	p := &process.Process{Pid: int32(pid)}
	return p.Cwd()
}
