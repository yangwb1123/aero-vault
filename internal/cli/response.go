package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func readSuccessfulResponse(resp *http.Response) ([]byte, bool) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read response:", err)
		return nil, false
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(body))
		return nil, false
	}
	return body, true
}

func printResponseBody(body []byte) {
	if len(body) > 0 {
		fmt.Println(string(body))
	}
}
