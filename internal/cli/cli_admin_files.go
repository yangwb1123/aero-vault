package cli

import (
	"fmt"
	"net/http"
	"os"
)

// cmdAdminFiles dispatches the `admin files` resource. Only the delete action
// is part of this direction's surface; unknown actions mirror the sibling
// admin resources (stderr + exit 2).
func (c *Client) cmdAdminFiles(action string, args []string) int {
	switch action {
	case "delete":
		return c.adminFilesDelete(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown files action: %s\n", action)
		return 2
	}
}

// adminFilesDelete implements `admin files delete <tenant> <key> [--hard]`:
// DELETE /v1/admin/files/<tenant>/<key>[?hard=1]. Tenant and key are escaped
// per path segment via escapeKey, so keys containing '/' are safe. An empty
// tenant is rejected with exit 2 before any request is built — otherwise the
// server-side defaults("") would silently target the default tenant (F13).
func (c *Client) adminFilesDelete(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin files delete <tenant> <key> [--hard]")
		return 2
	}
	if args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: admin files delete <tenant> <key> [--hard]  (tenant must not be empty)")
		return 2
	}
	hard := false
	for i := 2; i < len(args); i++ {
		if args[i] == "--hard" {
			hard = true
		}
	}
	path := "/v1/admin/files/" + escapeKey(args[0]+"/"+args[1])
	if hard {
		path += "?hard=1"
	}
	resp, err := c.do(http.MethodDelete, path, nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if _, ok := readSuccessfulResponse(resp); !ok {
		return 1
	}
	fmt.Println("deleted")
	return 0
}
