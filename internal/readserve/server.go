package readserve

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

type Config struct {
	Repo           string
	Base           string
	Head           string
	LogPath        string
	Call           string
	MaxResultBytes int
	MaxResultLines int
	PerCallBytes   int
	PerLegReads    int
	PerLegBytes    int
}

func parseArgs(args []string) Config {
	c := Config{
		MaxResultBytes: 12288,
		MaxResultLines: 400,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--repo" && i+1 < len(args) {
			c.Repo = args[i+1]
			i++
		} else if arg == "--base" && i+1 < len(args) {
			c.Base = args[i+1]
			i++
		} else if arg == "--head" && i+1 < len(args) {
			c.Head = args[i+1]
			i++
		} else if arg == "--log" && i+1 < len(args) {
			c.LogPath = args[i+1]
			i++
		} else if arg == "--call" && i+1 < len(args) {
			c.Call = args[i+1]
			i++
		} else if arg == "--max-result-bytes" && i+1 < len(args) {
			c.MaxResultBytes, _ = strconv.Atoi(args[i+1])
			i++
		} else if arg == "--max-result-lines" && i+1 < len(args) {
			c.MaxResultLines, _ = strconv.Atoi(args[i+1])
			i++
		} else if arg == "--per-call-bytes" && i+1 < len(args) {
			c.PerCallBytes, _ = strconv.Atoi(args[i+1])
			i++
		} else if arg == "--per-leg-reads" && i+1 < len(args) {
			c.PerLegReads, _ = strconv.Atoi(args[i+1])
			i++
		} else if arg == "--per-leg-bytes" && i+1 < len(args) {
			c.PerLegBytes, _ = strconv.Atoi(args[i+1])
			i++
		}
	}
	return c
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternalError  = -32603
)

func newErrorResponse(id json.RawMessage, code int, message string) JSONRPCResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
}

func newResultResponse(id json.RawMessage, result interface{}) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

type logger struct {
	path string
	f    *os.File
}

func (l *logger) event(name string, payload interface{}) {
	if l.f == nil {
		return
	}
	b, _ := json.Marshal(map[string]interface{}{
		"event":   name,
		"payload": payload,
	})
	_, _ = l.f.Write(append(b, '\n'))
}

func (l *logger) close() {
	if l.f != nil {
		l.f.Close()
	}
}

func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	cfg := parseArgs(args)

	var lg logger
	if cfg.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0755); err == nil {
			if f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
				lg.path = cfg.LogPath
				lg.f = f
			}
		}
	}
	defer lg.close()

	lg.event("start", cfg)

	git := vcs.New(exec.NewOSRunner(), nil)
	repo := git.At(cfg.Repo)
	bytesReadInCall := 0

	scanner := bufio.NewScanner(in)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	encoder := json.NewEncoder(out)

	for scanner.Scan() {
		line := scanner.Bytes()
		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = encoder.Encode(newErrorResponse(nil, ErrParseError, "Parse error"))
			continue
		}

		if req.JSONRPC != "2.0" {
			_ = encoder.Encode(newErrorResponse(req.ID, ErrInvalidRequest, "Invalid request"))
			continue
		}

		switch req.Method {
		case "initialize":
			lg.event("initialize", nil)
			_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05", // The negotiated MCP protocol version string the spike did not capture
				"serverInfo": map[string]interface{}{
					"name":    "crossrev-read-server",
					"version": "1.0.0",
				},
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
			}))
		case "notifications/initialized":
			// Do nothing
		case "tools/list":
			lg.event("tools_list", nil)
			_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
				"tools": []map[string]interface{}{
					{
						"name":        "read_file",
						"description": "Reads a file from the repository at a specific revision (base, head, or a SHA)",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"path": map[string]interface{}{
									"type": "string",
								},
								"revision": map[string]interface{}{
									"type": "string",
								},
								"start_line": map[string]interface{}{
									"type": "integer",
								},
								"end_line": map[string]interface{}{
									"type": "integer",
								},
							},
							"required": []string{"path", "revision"},
						},
					},
				},
			}))
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = encoder.Encode(newErrorResponse(req.ID, ErrInvalidParams, "Invalid params"))
				continue
			}

			if params.Name != "read_file" {
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": "Method not found: " + params.Name,
						},
					},
				}))
				continue
			}

			var argsStruct struct {
				Path      string `json:"path"`
				Revision  string `json:"revision"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
				Digest    string `json:"digest,omitempty"`
			}
			if err := json.Unmarshal(params.Arguments, &argsStruct); err != nil {
				_ = encoder.Encode(newErrorResponse(req.ID, ErrInvalidParams, "Invalid arguments"))
				continue
			}

			if strings.Contains(argsStruct.Path, "..") || filepath.IsAbs(argsStruct.Path) || strings.HasPrefix(argsStruct.Path, "/") {
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": "path_invalid"}},
				}))
				continue
			}

			var rev core.Revision
			var err error
			if argsStruct.Revision == "base" {
				rev, err = core.NewRevision(cfg.Base)
			} else if argsStruct.Revision == "head" {
				rev, err = core.NewRevision(cfg.Head)
			} else {
				rev, err = core.NewRevision(argsStruct.Revision)
			}

			if err != nil {
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": "outside_revisions"}},
				}))
				continue
			}

			lsOut, _ := repo.Run(ctx, "ls-tree", rev.String(), argsStruct.Path)
			if lsOut.OK() {
				parts := strings.Fields(lsOut.Text())
				if len(parts) >= 2 {
					if parts[0] == "120000" {
						_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
							"isError": true,
							"content": []map[string]interface{}{{"type": "text", "text": "symlink"}},
						}))
						continue
					}
					if parts[0] == "160000" {
						_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
							"isError": true,
							"content": []map[string]interface{}{{"type": "text", "text": "submodule"}},
						}))
						continue
					}
				}
			}

			// Show pairs a runner failure with a NotFound status rather than
			// leaving it unclassified, and the refusal contract carries one
			// of nine reasons rather than raw git text, so the status below
			// drives the refusal and the transport error is not stringified.
			bytesOut, fileStatus, _ := repo.Show(ctx, rev, argsStruct.Path)

			if fileStatus == vcs.NotFound {
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": "not_found"}},
				}))
				continue
			}
			if fileStatus == vcs.IsOther {
				// A tree holds no file content, and the contract has no
				// reason for one, so it refuses as not_found.
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": "not_found"}},
				}))
				continue
			}
			
			if bytes.Contains(bytesOut, []byte{0}) {
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": "binary"}},
				}))
				continue
			}

			lines := strings.Split(string(bytesOut), "\n")
			start := argsStruct.StartLine
			if start < 1 {
				start = 1
			}
			end := argsStruct.EndLine
			if end < start || end > len(lines) {
				end = len(lines)
			}
			if start > len(lines) {
				_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
					"isError": true,
					"content": []map[string]interface{}{{"type": "text", "text": "bad_range"}},
				}))
				continue
			}

			var sb strings.Builder
			for i := start - 1; i < end; i++ {
				lineBytes := len(lines[i]) + 1
				if bytesReadInCall + sb.Len() + lineBytes > cfg.PerCallBytes {
					// Budget exhausted
					_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
						"isError": true,
						"content": []map[string]interface{}{{"type": "text", "text": "budget_exhausted"}},
					}))
					goto nextReq
				}
				if i == start - 1 && lineBytes > cfg.MaxResultBytes {
					// Line itself is too long
					_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
						"isError": true,
						"content": []map[string]interface{}{{"type": "text", "text": fmt.Sprintf("line_too_long %d start_line=%d", lineBytes, i+2)}},
					}))
					goto nextReq
				}
				if sb.Len() + lineBytes > cfg.MaxResultBytes || (i - start + 1) >= cfg.MaxResultLines {
					// Cut before this line
					sb.WriteString(fmt.Sprintf("(cut, next start_line=%d)\n", i+1))
					break
				}
				sb.WriteString(fmt.Sprintf("%d: %s\n", i+1, lines[i]))
			}

			bytesReadInCall += sb.Len()
			_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": sb.String()}},
			}))
			
			h := sha256.New()
			h.Write([]byte(sb.String()))
			argsStruct.Digest = hex.EncodeToString(h.Sum(nil))
			lg.event("read", argsStruct)

		case "ping":
			_ = encoder.Encode(newResultResponse(req.ID, map[string]interface{}{}))

		default:
			_ = encoder.Encode(newErrorResponse(req.ID, ErrMethodNotFound, "Method not found"))
		}
	nextReq:
	}

	lg.event("end", nil)
	return 0
}
