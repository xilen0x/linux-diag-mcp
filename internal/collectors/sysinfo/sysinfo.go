// Package sysinfo reads the kernel release, uptime and OS identification.
package sysinfo

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strconv"
	"strings"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

// Info is the result of system_info.
type Info struct {
	KernelRelease string  `json:"kernel_release" jsonschema:"kernel release, from /proc/version"`
	UptimeSeconds float64 `json:"uptime_seconds" jsonschema:"seconds since boot"`
	OS            OS      `json:"os" jsonschema:"OS identification from /etc/os-release; empty if absent"`
}

// OS holds selected /etc/os-release fields.
type OS struct {
	Name       string `json:"name,omitempty"`
	PrettyName string `json:"pretty_name,omitempty"`
	ID         string `json:"id,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
	Version    string `json:"version,omitempty"`
}

// Collect reads "version" and "uptime" from procFS (rooted at /proc) and
// "os-release" from etcFS (rooted at /etc). A missing os-release is not an
// error.
func Collect(procFS, etcFS fs.FS) (Info, error) {
	release, err := kernelRelease(procFS)
	if err != nil {
		return Info{}, err
	}
	uptime, err := uptimeSeconds(procFS)
	if err != nil {
		return Info{}, err
	}
	osInfo, err := osRelease(etcFS)
	if err != nil {
		return Info{}, err
	}
	return Info{KernelRelease: release, UptimeSeconds: uptime, OS: osInfo}, nil
}

// kernelRelease extracts the release from "Linux version <release> ...".
func kernelRelease(procFS fs.FS) (string, error) {
	data, err := procio.ReadFile(procFS, "version", 4096)
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(data))
	if len(f) < 3 || f[0] != "Linux" || f[1] != "version" {
		return "", errors.New("parse /proc/version: unexpected format")
	}
	return procio.CleanText(f[2]), nil
}

func uptimeSeconds(procFS fs.FS) (float64, error) {
	data, err := procio.ReadFile(procFS, "uptime", 4096)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0, errors.New("parse /proc/uptime: empty")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("parse /proc/uptime: invalid uptime")
	}
	return v, nil
}

func osRelease(etcFS fs.FS) (OS, error) {
	data, err := procio.ReadFile(etcFS, "os-release", 64*1024)
	if errors.Is(err, fs.ErrNotExist) {
		return OS{}, nil
	}
	if err != nil {
		return OS{}, fmt.Errorf("os-release: %w", err)
	}
	var o OS
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		key, raw, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		val := procio.CleanText(unquote(raw))
		switch key {
		case "NAME":
			o.Name = val
		case "PRETTY_NAME":
			o.PrettyName = val
		case "ID":
			o.ID = val
		case "VERSION_ID":
			o.VersionID = val
		case "VERSION":
			o.Version = val
		}
	}
	return o, sc.Err()
}

// unquote handles os-release(5) values: shell-style single or double quotes,
// with \" \\ \$ \` escapes inside double quotes. Malformed values are
// returned as-is (they are sanitized by the caller anyway).
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	in := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		if in[i] == '\\' && i+1 < len(in) && strings.IndexByte("\"\\$`", in[i+1]) >= 0 {
			i++
		}
		b.WriteByte(in[i])
	}
	return b.String()
}
