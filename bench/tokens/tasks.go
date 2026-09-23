package tokens

import (
	"encoding/json"
	"strings"
)

const (
	serverGo = "net/http/server.go"
	h2Bundle = "net/http/h2_bundle.go" // 12,234 lines
	procGo   = "runtime/proc.go"       // 7,372 lines
	bogo     = "crypto/tls/bogo_config.json"
	bogoN    = 226 // entries in DisabledTests at the pin
)

// itoaCallers are the files calling itoa.Itoa at the pin (44 calls).
var itoaCallers = []string{
	"internal/itoa/itoa_test.go", "internal/poll/fd_io_plan9.go", "internal/poll/fd_unix.go",
	"net/dnsclient_unix.go", "net/interface_plan9.go", "net/ipsock_plan9.go", "net/lookup_plan9.go",
	"net/netip/netip.go", "net/tcpsock.go", "net/tcpsockopt_plan9.go", "net/udpsock.go",
	"os/exec_plan9.go", "os/exec_posix.go", "os/executable_plan9.go", "os/signal/signal_plan9_test.go",
	"reflect/value.go", "syscall/exec_linux.go", "syscall/exec_plan9.go", "syscall/syscall_js.go",
	"syscall/syscall_linux.go", "syscall/syscall_unix.go", "syscall/syscall_wasip1.go",
	"syscall/syscall_windows.go", "time/zoneinfo_js.go",
}

// Tasks is the fixed task set. IDs are stable; never renumber.
var Tasks = []Task{
	{
		ID: "T01", Category: FindEdit, Title: "Raise net/http's default header size limit",
		Prompt: "net/http limits request headers to 1 MB by default. Raise the default " +
			"to 2 MB, and keep the comment next to the value accurate.",
		Allowed: []string{serverGo},
		check: func(c *C) {
			c.WantConst(serverGo, "DefaultMaxHeaderBytes", 2<<20)
			c.Lacks(serverGo, "// 1 MB")
		},
		solve: func(r string) error {
			return replace(r, serverGo, "DefaultMaxHeaderBytes = 1 << 20 // 1 MB", "DefaultMaxHeaderBytes = 2 << 20 // 2 MB", 1)
		},
	},
	{
		ID: "T02", Category: FindEdit, Title: "Raise the unread-body limit for keep-alive",
		Prompt: "When a net/http handler does not read the whole request body, the server " +
			"reads up to 256 KB of what is left so it can keep the connection alive. " +
			"Raise that limit to 1 MB.",
		Allowed: []string{serverGo},
		check:   func(c *C) { c.WantConst(serverGo, "maxPostHandlerReadBytes", 1<<20) },
		solve: func(r string) error {
			return replace(r, serverGo, "maxPostHandlerReadBytes = 256 << 10", "maxPostHandlerReadBytes = 1 << 20", 1)
		},
	},
	{
		ID: "T03", Category: FindEdit, Title: "Change an error message",
		Prompt: "Change the text of the net/http error returned when a handler writes a body " +
			"for a request method or response status code that does not allow one. The new " +
			"text is: http: body not allowed for this request method or response status",
		Allowed: []string{serverGo},
		check: func(c *C) {
			c.Contains(serverGo, `ErrBodyNotAllowed = errors.New("http: body not allowed for this request method or response status")`)
		},
		solve: func(r string) error {
			return replace(r, serverGo, "request method or response status code does not allow body",
				"body not allowed for this request method or response status", 1)
		},
	},
	{
		ID: "T04", Category: FindEdit, Title: "Fix a comment typo",
		Prompt: "A comment in crypto/tls's TLS 1.3 server handshake code misspells " +
			"\"received\" as \"recieved\". Fix it.",
		Allowed: []string{"crypto/tls/handshake_server_tls13.go"},
		check: func(c *C) {
			c.WantWords("crypto/tls", "recieved", 0)
			c.Contains("crypto/tls/handshake_server_tls13.go", "client_hello we received contained")
		},
		solve: func(r string) error {
			return replace(r, "crypto/tls/handshake_server_tls13.go", "we recieved", "we received", 1)
		},
	},
	{
		ID: "T05", Category: LargeFile, Title: "Lower the HTTP/2 default max read frame size",
		Prompt: "The HTTP/2 implementation bundled in net/http uses a default maximum read " +
			"frame size of 1 MB. Lower that default to 512 KiB.",
		Allowed: []string{h2Bundle},
		check:   func(c *C) { c.WantConst(h2Bundle, "http2defaultMaxReadFrameSize", 512<<10) },
		solve: func(r string) error {
			return replace(r, h2Bundle, "http2defaultMaxReadFrameSize = 1 << 20", "http2defaultMaxReadFrameSize = 512 << 10", 1)
		},
	},
	{
		ID: "T06", Category: LargeFile, Title: "Shorten the HTTP/2 preface timeout",
		Prompt: "In net/http/h2_bundle.go the HTTP/2 server waits 10 seconds for the client " +
			"connection preface. Make it 5 seconds.",
		Allowed: []string{h2Bundle},
		check:   func(c *C) { c.WantConst(h2Bundle, "http2prefaceTimeout", 5e9) },
		solve: func(r string) error {
			return replace(r, h2Bundle, "http2prefaceTimeout        = 10 * time.Second", "http2prefaceTimeout        = 5 * time.Second", 1)
		},
	},
	{
		ID: "T07", Category: LargeFile, Title: "Rename a constant inside a large file",
		Prompt: "In net/http/h2_bundle.go, rename the constant http2handlerChunkWriteSize " +
			"to http2handlerChunkWriteBytes, including every use.",
		Allowed: []string{h2Bundle},
		check: func(c *C) {
			c.WantWords("net/http", "http2handlerChunkWriteSize", 0)
			c.WantWords("net/http", "http2handlerChunkWriteBytes", 2)
			c.WantConst(h2Bundle, "http2handlerChunkWriteBytes", 4<<10)
		},
		solve: func(r string) error {
			return replace(r, h2Bundle, "http2handlerChunkWriteSize", "http2handlerChunkWriteBytes", 2)
		},
	},
	{
		ID: "T08", Category: LargeFile, Title: "Change the runtime's forced GC period",
		Prompt: "The Go runtime forces a garbage collection if none has run for 2 minutes. " +
			"Change that period to 5 minutes.",
		Allowed: []string{procGo},
		check:   func(c *C) { c.WantConst(procGo, "forcegcperiod", 5*60*1e9) },
		solve: func(r string) error {
			return replace(r, procGo, "forcegcperiod int64 = 2 * 60 * 1e9", "forcegcperiod int64 = 5 * 60 * 1e9", 1)
		},
	},
	{
		ID: "T09", Category: Rename, Title: "Rename a function across a package",
		Prompt: "In encoding/json, rename the unexported function isValidNumber to " +
			"isValidJSONNumber everywhere in the package, tests included.",
		Allowed: []string{"encoding/json/"},
		check: func(c *C) {
			c.WantWords("encoding/json", "isValidNumber", 0)
			c.WantWords("encoding/json", "isValidJSONNumber", 9)
		},
		solve: func(r string) error {
			for f, n := range map[string]int{"encode.go": 5, "decode.go": 1, "bench_test.go": 1, "number_test.go": 2} {
				if err := replaceWord(r, "encoding/json/"+f, "isValidNumber", "isValidJSONNumber", n); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		ID: "T10", Category: Rename, Title: "Rename a function across packages",
		Prompt: "Rename internal/itoa's Uitox to UitoaHex, updating its doc comment, tests " +
			"and every caller in the tree.",
		Allowed: []string{"internal/itoa/", "os/exec_posix.go"},
		check: func(c *C) {
			c.WantWords("", "Uitox", 0)
			c.WantWords("", "UitoaHex", 5)
			c.Contains("internal/itoa/itoa.go", "// UitoaHex converts", "func UitoaHex(val uint) string {")
		},
		solve: func(r string) error {
			for f, n := range map[string]int{"internal/itoa/itoa.go": 2, "internal/itoa/itoa_test.go": 2, "os/exec_posix.go": 1} {
				if err := replaceWord(r, f, "Uitox", "UitoaHex", n); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		ID: "T11", Category: Rename, Title: "Rename a widely used function",
		Prompt: "Rename internal/itoa's Itoa to FormatInt. Update its doc comment and every " +
			"call site in the tree (there are dozens, across many packages). Leave Uitoa and " +
			"Uitox alone.",
		Allowed: append([]string{"internal/itoa/"}, itoaCallers...),
		check: func(c *C) {
			c.WantWords("", "itoa.Itoa", 0)
			c.WantWords("", "itoa.FormatInt", 44)
			c.Contains("internal/itoa/itoa.go", "// FormatInt converts", "func FormatInt(val int) string {", "func Uitoa(", "func Uitox(")
			c.Lacks("internal/itoa/itoa.go", "func Itoa(")
		},
		solve: func(r string) error {
			if err := replace(r, "internal/itoa/itoa.go", "Itoa converts", "FormatInt converts", 1); err != nil {
				return err
			}
			if err := replace(r, "internal/itoa/itoa.go", "func Itoa(", "func FormatInt(", 1); err != nil {
				return err
			}
			for _, f := range itoaCallers {
				if err := replace(r, f, "itoa.Itoa(", "itoa.FormatInt(", -1); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		ID: "T12", Category: MultiFile, Title: "Raise bufio's default buffer sizes",
		Prompt: "Raise bufio's default buffer size for Reader and Writer, and the initial " +
			"buffer allocation of Scanner, from 4096 to 8192 bytes.",
		Allowed: []string{"bufio/bufio.go", "bufio/scan.go"},
		check: func(c *C) {
			c.WantConst("bufio/bufio.go", "defaultBufSize", 8192)
			c.WantConst("bufio/scan.go", "startBufSize", 8192)
		},
		solve: func(r string) error {
			if err := replace(r, "bufio/bufio.go", "defaultBufSize = 4096", "defaultBufSize = 8192", 1); err != nil {
				return err
			}
			return replace(r, "bufio/scan.go", "startBufSize = 4096", "startBufSize = 8192", 1)
		},
	},
	{
		ID: "T13", Category: Insert, Title: "Add a method after an existing one",
		Prompt: "Add a method WriteLine(s string) (int, error) to strings.Builder that appends s " +
			"followed by a newline and returns the number of bytes written. Put it directly " +
			"after WriteString in strings/builder.go and call copyCheck first, like WriteString.",
		Allowed: []string{"strings/builder.go"},
		check: func(c *C) {
			_, f := c.Parse("strings/builder.go")
			if f == nil {
				return
			}
			fd, i := Func(f, "Builder", "WriteLine")
			_, ws := Func(f, "Builder", "WriteString")
			switch {
			case fd == nil:
				c.Errorf("no method (*Builder).WriteLine")
			case i != ws+1:
				c.Errorf("WriteLine is not directly after WriteString")
			case Signature(fd) != "(string) (int, error)":
				c.Errorf("WriteLine signature %s", Signature(fd))
			case !Uses(fd.Body, "copyCheck") || !HasNewline(fd.Body):
				c.Errorf("WriteLine does not call copyCheck or write a newline")
			}
		},
		solve: func(r string) error {
			return replace(r, "strings/builder.go", "\treturn len(s), nil\n}\n",
				"\treturn len(s), nil\n}\n\n// WriteLine appends s and a newline to b's buffer.\n"+
					"func (b *Builder) WriteLine(s string) (int, error) {\n\tb.copyCheck()\n"+
					"\tb.buf = append(b.buf, s...)\n\tb.buf = append(b.buf, '\\n')\n\treturn len(s) + 1, nil\n}\n", 1)
		},
	},
	{
		ID: "T14", Category: NewFile, Title: "Add a new file with a function",
		Prompt: "Create strings/cutfold.go in package strings with a function " +
			"CutPrefixFold(s, prefix string) (after string, found bool). It works like " +
			"CutPrefix but compares the prefix case-insensitively using EqualFold. Include " +
			"the usual copyright header and a doc comment.",
		Allowed: []string{"strings/cutfold.go"},
		check: func(c *C) {
			_, f := c.Parse("strings/cutfold.go")
			if f == nil {
				return
			}
			fd, _ := Func(f, "", "CutPrefixFold")
			switch {
			case f.Name.Name != "strings":
				c.Errorf("package %s, want strings", f.Name.Name)
			case fd == nil:
				c.Errorf("no func CutPrefixFold")
			case Signature(fd) != "(string, string) (string, bool)":
				c.Errorf("CutPrefixFold signature %s", Signature(fd))
			case !Uses(fd.Body, "EqualFold"):
				c.Errorf("CutPrefixFold does not use EqualFold")
			case fd.Doc == nil:
				c.Errorf("CutPrefixFold has no doc comment")
			}
		},
		solve: func(r string) error {
			return write(r, "strings/cutfold.go", `// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package strings

// CutPrefixFold is like [CutPrefix] but compares the prefix
// under simple Unicode case-folding, like [EqualFold].
func CutPrefixFold(s, prefix string) (after string, found bool) {
	if len(s) < len(prefix) || !EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
`)
		},
	},
	{
		ID: "T15", Category: Move, Title: "Move files",
		Prompt: "In encoding/json, rename tags.go to tagoptions.go and tags_test.go to " +
			"tagoptions_test.go. Do not change their contents.",
		Allowed: []string{"encoding/json/tags.go", "encoding/json/tags_test.go",
			"encoding/json/tagoptions.go", "encoding/json/tagoptions_test.go"},
		check: func(c *C) {
			c.Missing("encoding/json/tags.go")
			c.Missing("encoding/json/tags_test.go")
			c.SameAs("encoding/json/tagoptions.go", "encoding/json/tags.go")
			c.SameAs("encoding/json/tagoptions_test.go", "encoding/json/tags_test.go")
		},
		solve: func(r string) error {
			if err := move(r, "encoding/json/tags.go", "encoding/json/tagoptions.go"); err != nil {
				return err
			}
			return move(r, "encoding/json/tags_test.go", "encoding/json/tagoptions_test.go")
		},
	},
	{
		ID: "T16", Category: Delete, Title: "Remove debug-only code",
		Prompt: "internal/zstd has a debug constant that is always false and a debug-only " +
			"println guarded by it. Remove the constant, its comment, and the guarded code.",
		Allowed: []string{"internal/zstd/block.go"},
		check: func(c *C) {
			_, f := c.Parse("internal/zstd/block.go")
			if f != nil && Uses(f, "debug") {
				c.Errorf("internal/zstd/block.go still uses debug")
			}
			c.Lacks("internal/zstd/block.go", "println(", "// debug can be set")
		},
		solve: func(r string) error {
			if err := replace(r, "internal/zstd/block.go",
				"// debug can be set in the source to print debug info using println.\nconst debug = false\n\n", "", 1); err != nil {
				return err
			}
			return replace(r, "internal/zstd/block.go",
				"\t\tif debug {\n\t\t\tprintln(\"literal\", literal, \"offset\", offset, \"match\", match)\n\t\t}\n\n", "", 1)
		},
	},
	{
		ID: "T17", Category: Config, Title: "Bump a module requirement",
		Prompt: "Bump golang.org/x/crypto from v0.30.0 to v0.31.0 in the std module: update " +
			"go.mod and keep vendor/modules.txt consistent. Do not touch go.sum or the " +
			"vendored sources.",
		Allowed: []string{"go.mod", "vendor/modules.txt"},
		check: func(c *C) {
			c.Contains("go.mod", "golang.org/x/crypto v0.31.0\n")
			c.Lacks("go.mod", "v0.30.0")
			c.Contains("vendor/modules.txt", "# golang.org/x/crypto v0.31.0\n")
			c.Lacks("vendor/modules.txt", "crypto v0.30.0")
		},
		solve: func(r string) error {
			if err := replace(r, "go.mod", "golang.org/x/crypto v0.30.0", "golang.org/x/crypto v0.31.0", 1); err != nil {
				return err
			}
			return replace(r, "vendor/modules.txt", "# golang.org/x/crypto v0.30.0", "# golang.org/x/crypto v0.31.0", 1)
		},
	},
	{
		ID: "T18", Category: Config, Title: "Add an entry to a JSON config",
		Prompt: "In crypto/tls/bogo_config.json, disable the BoGo test " +
			"\"TLS-ECH-Server-LinoBench\" with the reason \"lino benchmark\". Keep the file " +
			"valid JSON and leave the other entries alone.",
		Allowed: []string{bogo},
		check: func(c *C) {
			dt := disabledTests(c)
			if dt == nil {
				return
			}
			if dt["TLS-ECH-Server-LinoBench"] != "lino benchmark" {
				c.Errorf("DisabledTests[TLS-ECH-Server-LinoBench] = %v", dt["TLS-ECH-Server-LinoBench"])
			}
			if len(dt) != bogoN+1 {
				c.Errorf("DisabledTests has %d entries, want %d", len(dt), bogoN+1)
			}
		},
		solve: func(r string) error {
			return replace(r, bogo, "\"DisabledTests\": {\n",
				"\"DisabledTests\": {\n        \"TLS-ECH-Server-LinoBench\": \"lino benchmark\",\n", 1)
		},
	},
	{
		ID: "T19", Category: Config, Title: "Remove entries from a JSON config",
		Prompt: "Go's TLS stack now supports channel ID. In crypto/tls/bogo_config.json, " +
			"remove every DisabledTests entry whose reason says channel ID is not supported. " +
			"Keep the file valid JSON.",
		Allowed: []string{bogo},
		check: func(c *C) {
			dt := disabledTests(c)
			if dt == nil {
				return
			}
			for k, v := range dt {
				if s, _ := v.(string); strings.Contains(s, "channel ID") {
					c.Errorf("entry %s still disabled: %s", k, s)
				}
			}
			if len(dt) != bogoN-3 {
				c.Errorf("DisabledTests has %d entries, want %d", len(dt), bogoN-3)
			}
		},
		solve: func(r string) error {
			return editLines(r, bogo, func(l string) bool { return !strings.Contains(l, "channel ID") })
		},
	},
}

func disabledTests(c *C) map[string]any {
	src, ok := c.Read(bogo)
	if !ok {
		return nil
	}
	var cfg struct {
		DisabledTests map[string]any
	}
	if err := json.Unmarshal([]byte(src), &cfg); err != nil {
		c.Errorf("%s is not valid JSON: %v", bogo, err)
		return nil
	}
	return cfg.DisabledTests
}
